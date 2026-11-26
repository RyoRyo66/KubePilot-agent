// Dedicated kind fixture: port-forwarded browser and a separate TLS machine endpoint.
import https from 'node:https';
import { readFileSync } from 'node:fs';
import { application } from '../server/main.js';
const app = application({dir:process.env.PLATFORM_DATA_DIR, publicOrigin:process.env.PLATFORM_PUBLIC_ORIGIN,
  allowHTTP:true, trustedModel:process.env.PLATFORM_TRUSTED_MODEL_URL, interval:3600000});
const tls = https.createServer({cert:readFileSync('/tls/tls.crt'),key:readFileSync('/tls/tls.key')},
  app.server.listeners('request')[0]);
app.server.listen(18081,'127.0.0.1');
tls.listen(18443,'0.0.0.0');
for (const signal of ['SIGTERM','SIGINT']) process.once(signal,()=>{tls.close(); void app.close();});
