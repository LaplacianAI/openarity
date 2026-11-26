import subprocess,sys
d='internal/triggers/watch/'
muts=[
('cooldown ready','watch.go','ready := lastRun == nil || !lastRun.Add(cooldown).After(now)','ready := true'),
('cooldown boundary','watch.go','!lastRun.Add(cooldown).After(now)','lastRun.Add(cooldown).Before(now)'),
('multiset diff','page.go','left[l]--\n\t\t\tcontinue','continue'),
('seen cap','feed.go','if len(ids) > maxSeen {','if false {'),
('page baseline','page.go','case "":\n\t\treturn next, "", false, true','case "x":\n\t\treturn next, "", false, true'),
('feed baseline','feed.go','if old.Seen == nil {','if len(old.Seen) == 0 {'),
('default port','watch.go','port != defaultPort[u.Scheme]','port != ""'),
('seen omitempty','watch.go','`json:"seen"`','`json:"seen,omitempty"`'),
('errors cleared','watch.go','next.ErrorsSince = nil','next.ErrorsSince = old.ErrorsSince'),
('pending kept','watch.go','next.Pending = old.Pending\n\t\treturn Result{Outcome: Unchanged','return Result{Outcome: Unchanged'),
('text cap','page.go','len(text) > maxText','len(text) > maxText*2'),
]
for name,f,a,b in muts:
    s=open(d+f).read()
    assert a in s,name
    open(d+f,'w').write(s.replace(a,b,1))
    r=subprocess.run(['go','test','-count=1','./'+d],capture_output=True,text=True)
    open(d+f,'w').write(s)
    fails=sorted({l.split()[2].split('/')[0] for l in r.stdout.splitlines() if l.startswith('--- FAIL')})
    print(name,'->',fails or ('NOT CAUGHT'+r.stdout[-200:]))
