import subprocess, re, csv, pathlib, hashlib, os, collections
CELL=re.compile(r"(?<!\\)\|"); ROW=re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")
def sh(*a): return subprocess.run(a,capture_output=True,text=True,timeout=180).stdout
def norm(s): return re.sub(r"\s+"," ",re.sub(r"[*`_]","",(s or "")).strip().lower())
IMG=(".png",".jpg",".jpeg",".webp")

canon={r["row_id"]:r for r in csv.DictReader(pathlib.Path("docs/ledger/uat-testcases.csv").open(newline="",encoding="utf-8"))}
days=[];seen=set()
for line in sh("git","log","--format=%H|%as","--","docs/ledger/UAT.md").strip().split("\n"):
    sha,day=line.split("|")
    if day in seen: continue
    seen.add(day); days.append((sha,day))
days=list(reversed(days))

# 57 git show, sonra hepsi bellekte
snaps={}
for sha,day in days:
    d={}
    for ln in sh("git","show",f"{sha}:docs/ledger/UAT.md").split("\n"):
        m=ROW.match(ln)
        if not m: continue
        c=CELL.split(ln.rstrip())
        if len(c)<8: continue
        d[c[1].strip()]=(norm(c[4]), next((g for g in "✅❌⚠️⛔☐" if g in c[6]),""), c[7].strip())
    snaps[day]=d
order=[d for _,d in days]

# artefakt kapsam onbellegi
cov_cache={}
def covers(path, rid):
    key=(path,rid)
    if key in cov_cache: return cov_cache[key]
    p=os.path.normpath(os.path.join("docs/ledger",path))
    if not os.path.exists(p): r="MISSING"
    elif p.lower().endswith(IMG): r="COVERS" if re.search(rf"(^|\D){rid}(\D|$)", os.path.basename(p)) else "NAMED-OTHER"
    elif os.path.isdir(p):
        r="COVERS" if any(re.search(rf"(^|\D){rid}(\D|$)",f) for f in os.listdir(p)) else "DIR-NO-MATCH"
    else:
        try: t=open(p,encoding="utf-8",errors="ignore").read()
        except Exception: t=""
        r="COVERS" if (re.search(rf"(row|tc)[ -]?{rid}\b",t,re.I) or re.search(rf"\b{rid}\s*[–-]\s*\d+",t)) else "NO-MENTION"
    cov_cache[key]=r; return r

out=[]
for rid,c in canon.items():
    txt=norm(c["test_case"])
    win=[]
    for day in reversed(order):
        s=snaps[day].get(rid)
        if not s or s[0]!=txt: break
        win.append(day)
    win.reverse()
    greens=[d for d in win if snaps[d][rid][1]=="✅"]
    backed=0; cur_backed=""
    for d in greens:
        ev=snaps[d][rid][2]
        L=[x for x in re.findall(r"\]\(([^)\s]+)\)",ev) if not x.startswith("http")]
        if any(covers(l,rid)=="COVERS" for l in L): backed+=1
    if win:
        last=win[-1]; ev=snaps[last][rid][2]
        L=[x for x in re.findall(r"\]\(([^)\s]+)\)",ev) if not x.startswith("http")]
        cur_backed = "YES" if any(covers(l,rid)=="COVERS" for l in L) else "no"
    out.append((rid,c["epic"],len(win),len(greens),backed,cur_backed,snaps[win[-1]][rid][1] if win else ""))

with open("/tmp/audit_all.csv","w",newline="") as fh:
    w=csv.writer(fh); w.writerow(["row_id","epic","identity_cycles","greens","greens_with_covering_artifact","current_verdict_backed","current_verdict"])
    w.writerows(out)

tot_g=sum(x[3] for x in out); tot_b=sum(x[4] for x in out)
print(f"286 test case, 57 cycle — HEPSI TEK GECISTE")
print(f"  kimlik penceresi ortalama : {sum(x[2] for x in out)/len(out):.1f} cycle")
print(f"  toplam yesil              : {tot_g}")
print(f"  KAPSAYICI artefakti olan  : {tot_b}  (%{100*tot_b/tot_g:.1f})")
print(f"  kanitsiz yesil            : {tot_g-tot_b}")
print()
cur=collections.Counter(x[5] for x in out if x[6]=="✅")
print(f"BUGUNKU verdict'i ✅ olan satirlar: {sum(cur.values())}")
print(f"  bunlarin kapsayici artefakti VAR : {cur.get('YES',0)}")
print(f"  YOK                              : {cur.get('no',0)}")
