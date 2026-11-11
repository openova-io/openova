package suite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/wordpress"
)

const (
	hrResource   = "helmreleases.helm.toolkit.fluxcd.io"
	cnpgResource = "clusters.postgresql.cnpg.io"
	appResource  = "applications.apps.openova.io"
)

// appView is GET /api/v1/sovereigns/{id}/applications/{name}.
type appView struct {
	Name, Namespace, UID, Phase string
	PrimaryRegion               string `json:"primaryRegion"`
	Conditions                  []struct {
		Type, Status, Reason, Message string
	} `json:"conditions"`
	DR *struct {
		Continuum             string `json:"continuum"`
		CurrentPrimary        string `json:"currentPrimary"`
		ObservedPrimaryRegion string `json:"observedPrimaryRegion"`
		Converged             bool   `json:"converged"`
		SwitchoverInProgress  bool   `json:"switchoverInProgress"`
		ConvergeMessage       string `json:"convergeMessage"`
	} `json:"dr"`
}

func (a appView) ready() (string, string) {
	for _, c := range a.Conditions {
		if c.Type == "Ready" {
			return c.Status, c.Reason + ": " + c.Message
		}
	}
	return "Unknown", "no Ready condition published"
}

// primary is the ACTUAL writer region the API reports: the Continuum's
// observation when there is one, never the declared placement.
func (a appView) primary() string {
	if a.DR != nil && a.DR.ObservedPrimaryRegion != "" {
		return a.DR.ObservedPrimaryRegion
	}
	return ""
}

func (r *runner) appPath(name string) string {
	return "/api/v1/sovereigns/" + url.PathEscape(r.dep) + "/applications/" + url.PathEscape(name)
}

func (r *runner) nsQuery() string { return "?namespace=" + url.QueryEscape(r.app.Namespace) }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// createBody is EXACTLY what NewInstanceDialog.onSubmit POSTs
// (InstancesSection.tsx), plus `environment`, which the ADR-0015 D4 contract
// requires (the current API ignores unknown fields, so sending it is safe on
// both sides of that change).
func (r *runner) createBody(name, org, env string, regions []string) map[string]any {
	return map[string]any{
		"blueprint":   strings.TrimPrefix(r.cfg.Blueprint, "bp-"),
		"org":         org,
		"environment": env,
		"name":        name,
		"topology":    "active-hot-standby",
		"placement":   map[string]any{"regions": regions},
	}
}

// ---------------------------------------------------------------- J1 install

func (r *runner) j1(ctx context.Context) bool {
	if r.cfg.AppName == "" {
		r.cfg.AppName = "accept-" + strconv.FormatInt(time.Now().Unix(), 10)
	}
	r.app.Name = r.cfg.AppName
	r.run.Subject["application"] = r.app.Name
	tag := strconv.FormatInt(time.Now().Unix(), 36)

	if !r.step("J1", "J1.0", "create with an unknown Org / Environment / Region → 422 each, nothing created", func(ev Evidence) error {
		neg := r.app.Name + "-neg"
		cases := []struct {
			what string
			body map[string]any
		}{
			{"organization", r.createBody(neg, "accept-noorg-"+tag, r.env.Name, []string{r.A.ID, r.B.ID})},
			{"environment", r.createBody(neg, r.org.Subdomain, "accept-noenv-"+tag, []string{r.A.ID, r.B.ID})},
			{"region", r.createBody(neg, r.org.Subdomain, r.env.Name, []string{r.A.ID, "accept-noregion-" + tag})},
		}
		codes := map[string]int{}
		var bad []string
		for _, c := range cases {
			res, err := r.api.Do(ctx, "POST", "/catalyst/v1/apps/instances", c.body)
			if err != nil {
				return err
			}
			codes[c.what] = res.Status
			if res.Status != 422 {
				bad = append(bad, fmt.Sprintf("unknown %s → HTTP %d (want 422)", c.what, res.Status))
			}
			if res.Status >= 200 && res.Status < 300 {
				// The API accepted a fabricated reference. Clean it up through
				// the same API so the run does not leave debris, then fail.
				_, _ = r.api.Do(ctx, "DELETE", r.appPath(neg), nil)
			}
		}
		ev["http"] = codes
		for _, reg := range []kube.Region{r.A, r.B} {
			ex, err := reg.NamespaceExists(ctx, "accept-noorg-"+tag)
			if err != nil {
				return err
			}
			if ex {
				bad = append(bad, "namespace accept-noorg-"+tag+" was created in "+reg.ID)
			}
			apps, err := listApplications(ctx, reg)
			if err != nil {
				return err
			}
			for _, a := range apps {
				if a.Name() == neg {
					bad = append(bad, "Application "+a.Namespace()+"/"+neg+" exists in "+reg.ID)
				}
			}
		}
		if len(bad) > 0 {
			return fmt.Errorf("%s", strings.Join(bad, "; "))
		}
		return nil
	}) {
		return false
	}

	if !r.step("J1", "J1.1", "create WordPress active-hot-standby, Primary A Standby B (POST /catalyst/v1/apps/instances)", func(ev Evidence) error {
		body := r.createBody(r.app.Name, r.org.Subdomain, r.env.Name, []string{r.A.ID, r.B.ID})
		ev["request"] = body
		res, err := r.api.Do(ctx, "POST", "/catalyst/v1/apps/instances", body)
		if err != nil {
			return err
		}
		ev["http"] = res.Status
		if res.Status >= 200 && res.Status < 300 {
			r.app.Created = true
		}
		if res.Status != 201 {
			return fmt.Errorf("HTTP %d (want 201): %s", res.Status, trunc(string(res.Body), 300))
		}
		return nil
	}) {
		return false
	}

	deadline := time.Now().Add(r.cfg.InstallTimeout)
	left := func() time.Duration { return time.Until(deadline) }

	if !r.step("J1", "J1.2", "Application Ready=True and placement reads Primary A / Standby B", func(ev Evidence) error {
		_, err := poll(ctx, left(), 10*time.Second, func() (bool, string, error) {
			var list struct {
				Items []struct{ Name, Namespace string } `json:"items"`
			}
			if _, err := r.api.Get(ctx, "/api/v1/sovereigns/"+url.PathEscape(r.dep)+"/applications", &list); err != nil {
				return false, err.Error(), nil
			}
			for _, a := range list.Items {
				if a.Name == r.app.Name {
					r.app.Namespace = a.Namespace
				}
			}
			if r.app.Namespace == "" {
				return false, "Application not yet listed", nil
			}
			var a appView
			if _, err := r.api.Get(ctx, r.appPath(r.app.Name)+r.nsQuery(), &a); err != nil {
				return false, err.Error(), nil
			}
			r.app.UID = a.UID
			st, why := a.ready()
			ev["phase"], ev["ready"], ev["reason"] = a.Phase, st, why
			return st == "True", fmt.Sprintf("Ready=%s %s", st, why), nil
		})
		if err != nil {
			return err
		}
		ev["namespace"], ev["uid"] = r.app.Namespace, r.app.UID
		var pl struct {
			Targets []Target `json:"targets"`
		}
		if _, err := r.api.Get(ctx, r.appPath(r.app.Name)+"/placement"+r.nsQuery(), &pl); err != nil {
			return err
		}
		ev["targets"] = pl.Targets
		return expectRoles(pl.Targets, r.A.ID, r.B.ID)
	}) {
		return false
	}

	if !r.step("J1", "J1.3", "HelmRelease Ready in EACH region (read from that region's cluster)", func(ev Evidence) error {
		_, err := poll(ctx, left(), 15*time.Second, func() (bool, string, error) {
			allOK := true
			var detail []string
			for _, reg := range []kube.Region{r.A, r.B} {
				hrs, err := r.hrsFor(ctx, reg)
				if err != nil {
					return false, err.Error(), nil
				}
				var rows []string
				if len(hrs) == 0 {
					allOK = false
					rows = append(rows, "none")
				}
				for _, h := range hrs {
					st, reason, msg, _ := h.Condition("Ready")
					rows = append(rows, fmt.Sprintf("%s/%s Ready=%s", h.Namespace(), h.Name(), nonEmpty(st, "Unknown")))
					if st != "True" {
						allOK = false
						rows[len(rows)-1] += " " + reason + ": " + trunc(msg, 120)
					}
				}
				ev[reg.ID] = rows
				detail = append(detail, reg.ID+": "+strings.Join(rows, ", "))
			}
			return allOK, strings.Join(detail, " | "), nil
		})
		return err
	}) {
		return false
	}

	if !r.step("J1", "J1.4", "CNPG Cluster present + healthy in EACH region; writer in A, replica in B (pg_is_in_recovery)", func(ev Evidence) error {
		_, err := poll(ctx, left(), 15*time.Second, func() (bool, string, error) {
			d, err := r.assertWriter(ctx, ev, r.A, r.B)
			if err != nil {
				return false, err.Error(), nil
			}
			return true, d, nil
		})
		return err
	}) {
		return false
	}

	if !r.step("J1", "J1.5", "app FQDN from the Application's endpoints (GET /catalyst/v1/apps/{uid}/endpoints)", func(ev Evidence) error {
		var eps struct {
			Items []struct {
				Hostname      string `json:"hostname"`
				LaunchDefault bool   `json:"launchDefault"`
				Status        string `json:"status"`
				Visibility    string `json:"visibility"`
			} `json:"items"`
		}
		_, err := poll(ctx, left(), 10*time.Second, func() (bool, string, error) {
			if _, err := r.api.Get(ctx, "/catalyst/v1/apps/"+url.PathEscape(r.app.UID)+"/endpoints", &eps); err != nil {
				return false, err.Error(), nil
			}
			for _, e := range eps.Items {
				if e.Hostname != "" && (e.LaunchDefault || r.app.FQDN == "") {
					r.app.FQDN = e.Hostname
				}
			}
			return r.app.FQDN != "", fmt.Sprintf("%d endpoints", len(eps.Items)), nil
		})
		ev["fqdn"], ev["endpoints"] = r.app.FQDN, eps.Items
		r.run.Subject["fqdn"] = r.app.FQDN
		return err
	}) {
		return false
	}

	return r.step("J1", "J1.6", "the FQDN serves a configured WordPress (installer completed if shown)", func(ev Evidence) error {
		r.app.Site = wordpress.New(r.app.FQDN, r.cfg.InsecureAppTLS)
		var home *wordpress.Page
		_, err := poll(ctx, left(), 10*time.Second, func() (bool, string, error) {
			p, err := r.app.Site.Get(ctx, "/")
			if err != nil {
				return false, err.Error(), nil
			}
			home = p
			return p.Status == 200, fmt.Sprintf("HTTP %d at %s", p.Status, p.FinalURL), nil
		})
		if err != nil {
			return err
		}
		ev["installer_shown"] = wordpress.NeedsInstall(home)
		if wordpress.NeedsInstall(home) {
			pass := randHex(16)
			if err := r.app.Site.Install(ctx, "acceptance "+r.run.RunID, "accept-admin", pass, "accept-admin@acceptance.invalid"); err != nil {
				return err
			}
			ev["installer_completed"] = true
			if home, err = r.app.Site.Get(ctx, "/"); err != nil {
				return err
			}
		}
		ev["status"], ev["final_url"] = home.Status, home.FinalURL
		if ok, why := wordpress.IsConfiguredHome(home); !ok {
			return fmt.Errorf("%s", why)
		}
		return nil
	})
}

func expectRoles(ts []Target, primary, standby string) error {
	roles := map[string]string{}
	for _, t := range ts {
		roles[t.Region] = t.Role
	}
	if roles[primary] != "Primary" || roles[standby] != "Standby" {
		return fmt.Errorf("placement targets %v: want %s=Primary %s=Standby", roles, primary, standby)
	}
	return nil
}

// ---------------------------------------------------------------- J2 data

func (r *runner) j2(ctx context.Context) bool {
	if r.app.Site == nil {
		r.skip("J2", "no site (J1 not run in this invocation)")
		return false
	}
	if !r.step("J2", "J2.1", "author credentials (application password) authenticate over HTTPS", func(ev Evidence) error {
		pod, err := r.wpPod(ctx, r.A)
		if err != nil {
			return err
		}
		login := "accept-bot"
		script := wordpress.EnsureAuthorScript(r.app.FQDN, login, randHex(16), r.run.RunID)
		out, err := r.A.Exec(ctx, pod.Namespace(), pod.Name(), "wordpress", strings.NewReader(script), "php")
		if err != nil {
			return err
		}
		pw, err := wordpress.ParseAppPassword(string(out))
		if err != nil {
			return err
		}
		r.app.Site.User, r.app.Site.AppPassword = login, pw
		ev["user"] = login
		ev["minted_in"] = r.A.ID + " " + pod.Namespace() + "/" + pod.Name()
		return r.app.Site.Whoami(ctx)
	}) {
		return false
	}

	marker := "accept-" + r.run.RunID + "-j2"
	if !r.step("J2", "J2.2", "publish a post with a unique marker; the commit returns within the budget", func(ev Evidence) error {
		p, d, err := r.app.Site.CreatePost(ctx, "acceptance "+marker, "<p>"+marker+"</p>")
		ev["duration_ms"] = d.Milliseconds()
		if err != nil {
			return err
		}
		ev["post_id"], ev["link"] = p.ID, p.Link
		if d > r.cfg.CommitBudget {
			return fmt.Errorf("write took %s > %s (a blocked synchronous commit looks exactly like this)", d.Round(time.Millisecond), r.cfg.CommitBudget)
		}
		r.app.Posts = append(r.app.Posts, writtenPost{ID: p.ID, Marker: marker, Region: r.A.ID})
		return nil
	}) {
		return false
	}

	if !r.step("J2", "J2.3", "upload a media file; the commit returns within the budget", func(ev Evidence) error {
		data, sum := wordpress.RandomPNG(marker)
		m, d, err := r.app.Site.UploadMedia(ctx, marker+".png", "image/png", data)
		ev["duration_ms"], ev["sha256"], ev["bytes"] = d.Milliseconds(), sum, len(data)
		if err != nil {
			return err
		}
		ev["media_id"], ev["source_url"] = m.ID, m.SourceURL
		r.app.MediaID, r.app.MediaURL, r.app.MediaSHA = m.ID, m.SourceURL, sum
		if d > r.cfg.CommitBudget {
			return fmt.Errorf("upload took %s > %s", d.Round(time.Millisecond), r.cfg.CommitBudget)
		}
		return nil
	}) {
		return false
	}

	if !r.step("J2", "J2.4", "read both back anonymously over HTTP (post shows the marker, media byte-equal)", func(ev Evidence) error {
		return r.readBack(ctx, ev)
	}) {
		return false
	}

	return r.step("J2", "J2.5", "the replica in B holds the post and the attachment (SELECT on B's instance)", func(ev Evidence) error {
		return r.rowsIn(ctx, ev, r.B, true)
	})
}

// readBack checks every post this run wrote + the media, anonymously.
func (r *runner) readBack(ctx context.Context, ev Evidence) error {
	posts := map[string]int{}
	for _, p := range r.app.Posts {
		ok, page, err := r.app.Site.ReadPostAnon(ctx, p.ID, p.Marker)
		if err != nil {
			return err
		}
		posts[strconv.Itoa(p.ID)] = page.Status
		if !ok {
			return fmt.Errorf("post %d (%s): HTTP %d, marker absent", p.ID, p.Marker, page.Status)
		}
	}
	ev["posts_http"] = posts
	sum, code, err := r.app.Site.FetchSHA256(ctx, r.app.MediaURL)
	if err != nil {
		return err
	}
	ev["media_http"], ev["media_sha256"] = code, sum
	if code != 200 || sum != r.app.MediaSHA {
		return fmt.Errorf("media %s: HTTP %d sha256=%s want %s", r.app.MediaURL, code, sum, r.app.MediaSHA)
	}
	return nil
}

// rowsIn asserts, with a SELECT on reg's instance, that every post and the
// attachment are present; wantStandby asserts the instance is in recovery.
func (r *runner) rowsIn(ctx context.Context, ev Evidence, reg kube.Region, wantStandby bool) error {
	prefix := r.cfg.WPTablePrefix
	_, err := poll(ctx, 90*time.Second, 5*time.Second, func() (bool, string, error) {
		cl, err := r.cnpgFor(ctx, reg)
		if err != nil {
			return false, err.Error(), nil
		}
		if len(cl) != 1 {
			return false, fmt.Sprintf("%d CNPG clusters for %s in %s", len(cl), r.app.Name, reg.ID), nil
		}
		c := cl[0]
		db := r.app.DBName
		pod := c.Str("status", "currentPrimary")
		rec, err := reg.PSQLSelect(ctx, c.Namespace(), pod, "postgres", "SELECT pg_is_in_recovery()")
		if err != nil {
			return false, err.Error(), nil
		}
		ev["instance"], ev["in_recovery"] = reg.ID+" "+c.Namespace()+"/"+pod, rec
		if (rec == "t") != wantStandby {
			return false, "", fmt.Errorf("%s in_recovery=%s, want standby=%v", pod, rec, wantStandby)
		}
		counts := map[string]string{}
		missing := []string{}
		for _, p := range r.app.Posts {
			n, err := reg.PSQLSelect(ctx, c.Namespace(), pod, db,
				fmt.Sprintf("SELECT count(*) FROM %sposts WHERE ID = %d AND post_content LIKE '%%%s%%'", prefix, p.ID, sqlLike(p.Marker)))
			if err != nil {
				return false, err.Error(), nil
			}
			counts[p.Marker] = n
			if n != "1" {
				missing = append(missing, p.Marker)
			}
		}
		if r.app.MediaID != 0 {
			n, err := reg.PSQLSelect(ctx, c.Namespace(), pod, db,
				fmt.Sprintf("SELECT count(*) FROM %sposts WHERE ID = %d AND post_type = 'attachment'", prefix, r.app.MediaID))
			if err != nil {
				return false, err.Error(), nil
			}
			counts["attachment"] = n
			if n != "1" {
				missing = append(missing, "attachment")
			}
		}
		ev["rows"] = counts
		return len(missing) == 0, fmt.Sprintf("missing in %s: %v", reg.ID, missing), nil
	})
	return err
}

// sqlLike keeps a marker safe inside a LIKE literal (markers are generated
// [a-z0-9-] but never trust that implicitly).
func sqlLike(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------- J3/J4 move

func (r *runner) move(ctx context.Context, j string, to, from kube.Region) bool {
	var saved time.Time
	if !r.step(j, j+".1", "Save placement with Primary "+to.ID+" (PUT …/applications/{name}, the PlacementEditor call)", func(ev Evidence) error {
		var pl struct {
			Targets []Target `json:"targets"`
		}
		if _, err := r.api.Get(ctx, r.appPath(r.app.Name)+"/placement"+r.nsQuery(), &pl); err != nil {
			return err
		}
		next, err := MovePrimary(pl.Targets, to.ID)
		if err != nil {
			return err
		}
		body := map[string]any{"placement": map[string]any{"targets": next, "ownedDependencies": []any{}}}
		ev["request"] = body
		saved = time.Now().UTC()
		res, err := r.api.Do(ctx, "PUT", r.appPath(r.app.Name)+r.nsQuery(), body)
		if err != nil {
			return err
		}
		ev["http"], ev["saved_at"] = res.Status, saved.Format(time.RFC3339)
		if res.Status < 200 || res.Status > 299 {
			return fmt.Errorf("HTTP %d: %s", res.Status, trunc(string(res.Body), 300))
		}
		return nil
	}) {
		return false
	}

	deadline := time.Now().Add(r.cfg.MoveTimeout)
	left := func() time.Duration { return time.Until(deadline) }

	if !r.step(j, j+".2", "Application + Continuum report ACTUAL primary = "+to.ID, func(ev Evidence) error {
		_, err := poll(ctx, left(), 10*time.Second, func() (bool, string, error) {
			var a appView
			if _, err := r.api.Get(ctx, r.appPath(r.app.Name)+r.nsQuery(), &a); err != nil {
				return false, err.Error(), nil
			}
			if a.DR == nil || a.DR.Continuum == "" {
				return false, "Application reports no dr/continuum — the actual primary is not observable", nil
			}
			ev["application_actual_primary"], ev["converged"] = a.primary(), a.DR.Converged
			if a.primary() != to.ID || !a.DR.Converged || a.DR.SwitchoverInProgress {
				return false, fmt.Sprintf("app actual=%s converged=%v inProgress=%v %s", a.primary(), a.DR.Converged, a.DR.SwitchoverInProgress, a.DR.ConvergeMessage), nil
			}
			var c struct {
				Status struct {
					PrimaryRegion        string `json:"primaryRegion"`
					Converged            bool   `json:"converged"`
					SwitchoverInProgress bool   `json:"switchoverInProgress"`
					LeaseHolder          string `json:"leaseHolder"`
				} `json:"status"`
			}
			if _, err := r.api.Get(ctx, "/api/v1/sovereigns/"+url.PathEscape(r.dep)+"/continuums/"+url.PathEscape(a.DR.Continuum)+r.nsQuery(), &c); err != nil {
				return false, err.Error(), nil
			}
			ev["continuum"], ev["continuum_primary"], ev["lease_holder"] = a.DR.Continuum, c.Status.PrimaryRegion, c.Status.LeaseHolder
			ok := c.Status.PrimaryRegion == to.ID && c.Status.Converged && !c.Status.SwitchoverInProgress
			return ok, fmt.Sprintf("continuum primary=%s converged=%v inProgress=%v", c.Status.PrimaryRegion, c.Status.Converged, c.Status.SwitchoverInProgress), nil
		})
		return err
	}) {
		return false
	}

	if !r.step(j, j+".3", "Jobs holds a complete topology-transition record for this move (every step succeeded)", func(ev Evidence) error {
		var v TransitionVerdict
		_, err := poll(ctx, left(), 10*time.Second, func() (bool, string, error) {
			var jobs struct {
				Jobs []Job `json:"jobs"`
			}
			if _, err := r.api.Get(ctx, "/api/v1/deployments/"+url.PathEscape(r.dep)+"/jobs", &jobs); err != nil {
				return false, err.Error(), nil
			}
			v = FindTransition(jobs.Jobs, r.app.Name, saved)
			return v.Done, v.Detail, nil
		})
		ev["record"], ev["steps"] = v.Parent, v.Steps
		return err
	}) {
		return false
	}

	if !r.step(j, j+".4", "database writer is "+to.ID+", replica is "+from.ID+" (pg_is_in_recovery on each)", func(ev Evidence) error {
		_, err := poll(ctx, left(), 10*time.Second, func() (bool, string, error) {
			d, err := r.assertWriter(ctx, ev, to, from)
			if err != nil {
				return false, err.Error(), nil
			}
			return true, d, nil
		})
		return err
	}) {
		return false
	}

	if !r.step(j, j+".5", "DNS for the app FQDN answers with "+to.ID+"'s address", func(ev Evidence) error {
		_, err := poll(ctx, left(), 10*time.Second, func() (bool, string, error) {
			ans, err := r.lookup(ctx, r.app.FQDN)
			ev["answers"] = ans
			if err != nil {
				return false, err.Error(), nil
			}
			if err := DNSServesRegion(ans, r.regionIPs, to.ID); err != nil {
				if strings.Contains(err.Error(), "share") || strings.Contains(err.Error(), "no public IPs") {
					return false, "", err // undecidable — waiting cannot fix it
				}
				return false, err.Error(), nil
			}
			return true, "", nil
		})
		return err
	}) {
		return false
	}

	if !r.step(j, j+".6", "the site serves the same posts and byte-equal media after the move", func(ev Evidence) error {
		_, err := poll(ctx, left(), 10*time.Second, func() (bool, string, error) {
			if err := r.readBack(ctx, ev); err != nil {
				return false, err.Error(), nil
			}
			return true, "", nil
		})
		return err
	}) {
		return false
	}

	return r.step(j, j+".7", "a new write succeeds in "+to.ID+" and replicates to "+from.ID, func(ev Evidence) error {
		marker := "accept-" + r.run.RunID + "-" + strings.ToLower(j)
		p, d, err := r.app.Site.CreatePost(ctx, "acceptance "+marker, "<p>"+marker+"</p>")
		ev["duration_ms"] = d.Milliseconds()
		if err != nil {
			return err
		}
		ev["post_id"] = p.ID
		if d > r.cfg.CommitBudget {
			return fmt.Errorf("write took %s > %s", d.Round(time.Millisecond), r.cfg.CommitBudget)
		}
		r.app.Posts = append(r.app.Posts, writtenPost{ID: p.ID, Marker: marker, Region: to.ID})
		w := Evidence{}
		if err := r.rowsIn(ctx, w, to, false); err != nil {
			return fmt.Errorf("writer %s: %w", to.ID, err)
		}
		s := Evidence{}
		if err := r.rowsIn(ctx, s, from, true); err != nil {
			return fmt.Errorf("replica %s: %w", from.ID, err)
		}
		ev["writer"], ev["replica"] = w, s
		return nil
	})
}

// ---------------------------------------------------------------- J5 cleanup

func (r *runner) j5(ctx context.Context) bool {
	if !r.step("J5", "J5.1", "delete the Application through the API", func(ev Evidence) error {
		res, err := r.api.Do(ctx, "DELETE", r.appPath(r.app.Name)+r.nsQuery(), nil)
		if err != nil {
			return err
		}
		ev["http"] = res.Status
		if res.Status < 200 || res.Status > 299 {
			return fmt.Errorf("HTTP %d: %s", res.Status, trunc(string(res.Body), 300))
		}
		return nil
	}) {
		return false
	}
	return r.step("J5", "J5.2", "both regions: Application, HelmReleases, CNPG Clusters and PVCs are gone", func(ev Evidence) error {
		_, err := poll(ctx, r.cfg.DeleteTimeout, 15*time.Second, func() (bool, string, error) {
			left := map[string][]string{}
			for _, reg := range []kube.Region{r.A, r.B} {
				var rest []string
				apps, err := listApplications(ctx, reg)
				if err != nil {
					return false, err.Error(), nil
				}
				for _, a := range apps {
					if a.Name() == r.app.Name {
						rest = append(rest, "Application/"+a.Namespace()+"/"+a.Name())
					}
				}
				hrs, err := r.hrsFor(ctx, reg)
				if err != nil {
					return false, err.Error(), nil
				}
				for _, h := range hrs {
					rest = append(rest, "HelmRelease/"+h.Namespace()+"/"+h.Name())
				}
				for _, kind := range []string{cnpgResource, "persistentvolumeclaims"} {
					objs, err := reg.GetList(ctx, kind, "")
					if err != nil {
						return false, err.Error(), nil
					}
					for _, o := range objs {
						if o.Mentions(r.app.Name) {
							rest = append(rest, kind+"/"+o.Namespace()+"/"+o.Name())
						}
					}
				}
				sort.Strings(rest)
				left[reg.ID] = rest
			}
			ev["remaining"] = left
			n := len(left[r.A.ID]) + len(left[r.B.ID])
			return n == 0, fmt.Sprintf("%v", left), nil
		})
		if err != nil {
			return err
		}
		res, err := r.api.Do(ctx, "GET", r.appPath(r.app.Name)+r.nsQuery(), nil)
		if err != nil {
			return err
		}
		ev["api_get_after_delete"] = res.Status
		if res.Status != 404 {
			return fmt.Errorf("GET after delete: HTTP %d (want 404)", res.Status)
		}
		return nil
	})
}

// ---------------------------------------------------------------- lookups

// hrsFor lists the HelmReleases of the app in ONE region.
func (r *runner) hrsFor(ctx context.Context, reg kube.Region) ([]kube.Object, error) {
	all, err := reg.GetList(ctx, hrResource, "")
	if err != nil {
		return nil, err
	}
	var out []kube.Object
	for _, h := range all {
		if h.Mentions(r.app.Name) || strings.Contains(h.Str("spec", "releaseName"), r.app.Name) {
			out = append(out, h)
		}
	}
	return out, nil
}

// cnpgFor lists the app's CNPG Clusters in ONE region — the namespaces its
// HelmReleases install into, matched by name/label/annotation or the Helm
// release-name annotation of one of those releases.
func (r *runner) cnpgFor(ctx context.Context, reg kube.Region) ([]kube.Object, error) {
	hrs, err := r.hrsFor(ctx, reg)
	if err != nil {
		return nil, err
	}
	nss, rels := map[string]bool{}, map[string]bool{}
	for _, h := range hrs {
		nss[nonEmpty(h.Str("spec", "targetNamespace"), h.Namespace())] = true
		rels[nonEmpty(h.Str("spec", "releaseName"), h.Name())] = true
	}
	var out []kube.Object
	for ns := range nss {
		cl, err := reg.GetList(ctx, cnpgResource, ns)
		if err != nil {
			return nil, err
		}
		for _, c := range cl {
			if c.Mentions(r.app.Name) || rels[c.Str("metadata", "annotations", "meta.helm.sh/release-name")] {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// assertWriter: exactly one healthy app Cluster per region, the writer's
// instance NOT in recovery, the standby's instance in recovery. The region is
// the kubeconfig that returned the object.
func (r *runner) assertWriter(ctx context.Context, ev Evidence, writer, standby kube.Region) (string, error) {
	var parts []string
	for _, side := range []struct {
		reg     kube.Region
		standby bool
	}{{writer, false}, {standby, true}} {
		cl, err := r.cnpgFor(ctx, side.reg)
		if err != nil {
			return "", err
		}
		if len(cl) != 1 {
			names := []string{}
			for _, c := range cl {
				names = append(names, c.Namespace()+"/"+c.Name())
			}
			return "", fmt.Errorf("%s: %d CNPG clusters for %s %v (want exactly 1 — each region renders only its own half)", side.reg.ID, len(cl), r.app.Name, names)
		}
		c := cl[0]
		healthy, d := ClusterHealthy(c)
		if !healthy {
			return "", fmt.Errorf("%s: %s", side.reg.ID, d)
		}
		if r.app.DBName == "" {
			r.app.DBName = nonEmpty(c.Str("spec", "bootstrap", "initdb", "database"), "app")
		}
		pod := c.Str("status", "currentPrimary")
		rec, err := side.reg.PSQLSelect(ctx, c.Namespace(), pod, "postgres", "SELECT pg_is_in_recovery()")
		if err != nil {
			return "", err
		}
		role := "writer"
		if side.standby {
			role = "replica"
		}
		ev[side.reg.ID] = map[string]any{"role": role, "cluster": c.Namespace() + "/" + c.Name(), "instance": pod,
			"in_recovery": rec, "declared_writer": DeclaredWriter(c), "health": d}
		if (rec == "t") != side.standby {
			return "", fmt.Errorf("%s: %s pg_is_in_recovery=%s — want %s", side.reg.ID, pod, rec, role)
		}
		parts = append(parts, side.reg.ID+"="+role)
	}
	return strings.Join(parts, " "), nil
}

// wpPod returns a Running+ready WordPress pod of the app in reg.
func (r *runner) wpPod(ctx context.Context, reg kube.Region) (kube.Object, error) {
	hrs, err := r.hrsFor(ctx, reg)
	if err != nil {
		return nil, err
	}
	nss, rels := map[string]bool{}, map[string]bool{}
	for _, h := range hrs {
		nss[nonEmpty(h.Str("spec", "targetNamespace"), h.Namespace())] = true
		rels[nonEmpty(h.Str("spec", "releaseName"), h.Name())] = true
	}
	for ns := range nss {
		pods, err := reg.GetList(ctx, "pods", ns)
		if err != nil {
			return nil, err
		}
		for _, p := range pods {
			if !(p.Mentions(r.app.Name) || rels[p.Str("metadata", "labels", "app.kubernetes.io/instance")]) {
				continue
			}
			if p.Str("status", "phase") != "Running" {
				continue
			}
			cs, _ := p.Get("status", "containerStatuses").([]any)
			for _, c := range cs {
				m, _ := c.(map[string]any)
				if m["name"] == "wordpress" && m["ready"] == true {
					return p, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("%s: no ready WordPress pod for %s in %v", reg.ID, r.app.Name, keys(nss))
}

func (r *runner) lookup(ctx context.Context, host string) ([]string, error) {
	res := net.DefaultResolver
	if r.cfg.DNSServer != "" {
		srv := r.cfg.DNSServer
		if !strings.Contains(srv, ":") {
			srv += ":53"
		}
		res = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, srv)
		}}
	}
	addrs, err := res.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range addrs {
		if v4 := a.IP.To4(); v4 != nil {
			out = append(out, v4.String())
		}
	}
	sort.Strings(out)
	return out, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// listApplications lists Application CRs in one region. Under ADR-0015 D1 only
// the region that runs catalyst-api carries the Application CRD; every other
// region receives the app as region files (HelmRelease + Continuum). There,
// "the server doesn't have a resource type" means no Application can exist,
// so it is an empty list, not an error. Every other error stays fatal.
func listApplications(ctx context.Context, reg kube.Region) ([]kube.Object, error) {
	apps, err := reg.GetList(ctx, appResource, "")
	if err != nil && strings.Contains(err.Error(), "doesn't have a resource type") {
		return nil, nil
	}
	return apps, err
}
