import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

// Only called after the acceptance driver verifies its dedicated kind context.
export function acceptanceTLS(output, namespace) {
  const dir=mkdtempSync(join(output,'tls-'));
  const ssl=(...args)=>execFileSync(process.env.OPENSSL || 'openssl',args,{cwd:dir,stdio:'pipe',windowsHide:true});
  ssl('req','-new','-x509','-nodes','-newkey','rsa:2048','-days','3','-subj','/CN=KubePilot acceptance CA',
    '-addext','basicConstraints=critical,CA:TRUE','-keyout','ca.key','-out','ca.crt');
  const caFile=join(dir,'ca.crt'), ca=readFileSync(caFile,'utf8');
  return {caFile,ca,secret(name,ns) {
    if(ns!==namespace) throw Error('测试证书命名空间不匹配');
    const host=name+'.'+ns+'.svc';
    ssl('req','-new','-nodes','-newkey','rsa:2048','-subj','/CN='+host,'-keyout',name+'.key','-out',name+'.csr');
    writeFileSync(join(dir,name+'.ext'),'subjectAltName=DNS:'+host+'\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\n');
    ssl('x509','-req','-in',name+'.csr','-CA','ca.crt','-CAkey','ca.key','-CAcreateserial','-days','3',
      '-extfile',name+'.ext','-out',name+'.crt');
    return {apiVersion:'v1',kind:'Secret',type:'kubernetes.io/tls',metadata:{name:name+'-tls',namespace:ns},
      data:{'tls.crt':readFileSync(join(dir,name+'.crt')).toString('base64'),'tls.key':readFileSync(join(dir,name+'.key')).toString('base64')}};
  }};
}
