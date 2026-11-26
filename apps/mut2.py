import subprocess,sys
d='internal/triggers/watch/'
muts=[('default port https','watch.go','"https": "443"','"https": "4430"'),('default port http','watch.go','"http": "80"','"http": "8"')]
for name,f,a,b in muts:
    s=open(d+f).read()
    assert a in s,name
    open(d+f,'w').write(s.replace(a,b,1))
    r=subprocess.run(['go','test','-count=1','./'+d],capture_output=True,text=True)
    open(d+f,'w').write(s)
    fails=sorted({l.split()[2].split('/')[0] for l in r.stdout.splitlines() if l.startswith('--- FAIL')})
    print(name,'->',fails or ('NOT CAUGHT'+r.stdout[-200:]))
