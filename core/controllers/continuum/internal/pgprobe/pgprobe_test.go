package pgprobe

import "testing"

// TestDerivePosture folds representative pg_stat_replication snapshots
// into a Posture. These are the exact interpretations the #5311 probe
// relies on: a streaming standby present → available + lag; zero rows →
// absent (the region-kill signal); column-masking never false-degrades.
func TestDerivePosture(t *testing.T) {
	t.Parallel()
	const replica = "cnpg-pair-bp-cnpg-pair-continuum-replica"

	cases := []struct {
		name        string
		rows        []ReplicationRow
		expectedApp string
		want        Posture
	}{
		{
			name: "streaming sync standby present → available + lag",
			rows: []ReplicationRow{
				{ApplicationName: replica, State: "streaming", SyncState: "sync", ReplayLagSeconds: 3, HasReplayLag: true},
			},
			expectedApp: replica,
			want:        Posture{StandbyPresent: true, Streaming: true, SyncStandbyPresent: true, ReplayLagSeconds: 3, AppName: replica},
		},
		{
			name:        "zero rows → standby ABSENT (region-kill signal)",
			rows:        nil,
			expectedApp: replica,
			want:        Posture{}, // StandbyPresent stays false
		},
		{
			name: "catchup standby present (not yet streaming) → present, not streaming",
			rows: []ReplicationRow{
				{ApplicationName: replica, State: "catchup", SyncState: "async", ReplayLagSeconds: 120, HasReplayLag: true},
			},
			expectedApp: replica,
			want:        Posture{StandbyPresent: true, Streaming: false, SyncStandbyPresent: false, ReplayLagSeconds: 120, AppName: replica},
		},
		{
			name: "quorum sync_state counts as a sync standby",
			rows: []ReplicationRow{
				{ApplicationName: replica, State: "streaming", SyncState: "quorum", HasReplayLag: false},
			},
			expectedApp: replica,
			want:        Posture{StandbyPresent: true, Streaming: true, SyncStandbyPresent: true, ReplayLagSeconds: 0, AppName: replica},
		},
		{
			name: "masked application_name still reads present (no false-degrade)",
			rows: []ReplicationRow{
				// streaming_replica probe role could see the walsender row
				// but have its application_name column NULL-masked → "".
				{ApplicationName: "", State: "streaming", SyncState: "sync", ReplayLagSeconds: 1, HasReplayLag: true},
			},
			expectedApp: replica,
			want:        Posture{StandbyPresent: true, Streaming: true, SyncStandbyPresent: true, ReplayLagSeconds: 1, AppName: ""},
		},
		{
			name: "empty expectedApp accepts any connected standby",
			rows: []ReplicationRow{
				{ApplicationName: "some-other-standby", State: "streaming", SyncState: "async", HasReplayLag: false},
			},
			expectedApp: "",
			want:        Posture{StandbyPresent: true, Streaming: true, AppName: "some-other-standby"},
		},
		{
			name: "max lag aggregated across multiple standbys; expected app wins AppName",
			rows: []ReplicationRow{
				{ApplicationName: "other", State: "streaming", SyncState: "async", ReplayLagSeconds: 2, HasReplayLag: true},
				{ApplicationName: replica, State: "streaming", SyncState: "sync", ReplayLagSeconds: 7, HasReplayLag: true},
			},
			expectedApp: replica,
			want:        Posture{StandbyPresent: true, Streaming: true, SyncStandbyPresent: true, ReplayLagSeconds: 7, AppName: replica},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DerivePosture(tc.rows, tc.expectedApp)
			if got != tc.want {
				t.Errorf("DerivePosture(%+v, %q)\n got  %+v\n want %+v", tc.rows, tc.expectedApp, got, tc.want)
			}
		})
	}
}
