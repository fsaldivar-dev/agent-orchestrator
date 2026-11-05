// Internal v0.14.9 build: retain the official desktop resources, install our Go
// daemon, and apply the byte-equivalent compiled form of daemon-launch.ts's
// cwd: homeDir fix. Fail closed on any upstream drift. No source install edits.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const {execFileSync} = require('node:child_process');
const os = require('node:os');
const [sourceApp, targetApp, asarModule] = process.argv.slice(2);
if (process.platform !== 'darwin' || !sourceApp || !targetApp || !asarModule) {
  throw new Error('Usage: node package-macos.cjs OFFICIAL.app FORK.app /path/to/@electron/asar');
}
if (fs.existsSync(targetApp)) throw new Error('Destination exists; refusing to replace an app');
const asar = require(asarModule);
const archive = path.join(sourceApp,'Contents/Resources/app.asar');
const raw = asar.getRawHeader(archive);
const sha = data => crypto.createHash('sha256').update(data).digest('hex');
if (sha(raw.headerString) !== '1e825fd8ed6b5fc003c7a72163e4d597381f6ad77e1a4fd21603ced28dde4ddd') {
  throw new Error('Expected the verified official v0.14.9 desktop archive');
}
const file = '.vite/build/main.js';
const original = asar.extractFile(archive,file);
const oldCode = 'cwd:r,shell:!0,source:`configured`';
const newCode = 'cwd:i,shell:!0,source:`configured`';
const text = original.toString('utf8');
if (text.split(oldCode).length !== 2) throw new Error('Expected exactly one configured-daemon cwd');
const patched = Buffer.from(text.replace(oldCode,newCode));
if (patched.length !== original.length) throw new Error('Patch must preserve archive offsets');
const buffer = fs.readFileSync(archive);
const stat = asar.statFile(archive,file);
const offset = 8 + raw.headerSize + Number(stat.offset);
if (!buffer.subarray(offset,offset+original.length).equals(original)) throw new Error('Archive offset validation failed');
const header = raw.header;
const entry = header.files['.vite'].files.build.files['main.js'];
entry.integrity.hash = sha(patched);
entry.integrity.blocks = [];
for(let i=0;i<patched.length;i+=entry.integrity.blockSize) entry.integrity.blocks.push(sha(patched.subarray(i,i+entry.integrity.blockSize)));
const newHeader = Buffer.from(JSON.stringify(header));
const oldHeader = Buffer.from(raw.headerString);
const headerOffset = buffer.indexOf(oldHeader);
if (newHeader.length !== oldHeader.length || headerOffset < 8 || headerOffset > 32) throw new Error('Archive header validation failed');
newHeader.copy(buffer,headerOffset);
patched.copy(buffer,offset);
execFileSync('/usr/bin/ditto',[sourceApp,targetApp]);
const targetArchive=path.join(targetApp,'Contents/Resources/app.asar');
fs.writeFileSync(targetArchive,buffer);
const plist=path.join(targetApp,'Contents/Info.plist');
const integrity={'Resources/app.asar':{algorithm:'SHA256',hash:sha(newHeader)}};
execFileSync('/usr/bin/plutil',['-replace','ElectronAsarIntegrity','-json',JSON.stringify(integrity),plist]);
execFileSync('/usr/bin/plutil',['-replace','CFBundleDisplayName','-string','Orchestrator Fork',plist]);
// Preserve CFBundleName: Electron uses it to locate its existing helper apps.
execFileSync('/usr/bin/plutil',['-replace','CFBundleIdentifier','-string','dev.agent-orchestrator.fork',plist]);
const daemon=path.resolve(__dirname,'../../frontend/daemon/ao');
fs.copyFileSync(daemon,path.join(targetApp,'Contents/Resources/daemon/ao'));
fs.chmodSync(path.join(targetApp,'Contents/Resources/daemon/ao'),0o755);
if (!asar.extractFile(targetArchive,file).equals(patched)) throw new Error('Repacked script did not verify');
// Ad-hoc local signing; this is intentionally not represented as notarization.
const entitlementsDir=fs.mkdtempSync(path.join(os.tmpdir(),'ao-fork-sign-'));
const apps=[];
function findApps(dir) {
  for (const item of fs.readdirSync(dir,{withFileTypes:true})) {
    if (!item.isDirectory()) continue;
    const child=path.join(dir,item.name);
    findApps(child);
    if (item.name.endsWith('.app')) apps.push(child);
  }
}
findApps(targetApp); apps.push(targetApp);
for (const [index,app] of apps.entries()) {
  const originalApp=path.join(sourceApp,path.relative(targetApp,app));
  const entitlements=execFileSync('/usr/bin/codesign',['-d','--entitlements',':-',originalApp],{stdio:['ignore','pipe','ignore']});
  const args=['--force','--sign','-','--options','0'];
  if (entitlements.length) {
    const file=path.join(entitlementsDir,index+'.plist');fs.writeFileSync(file,entitlements);
    args.push('--entitlements',file);
  }
  execFileSync('/usr/bin/codesign',[...args,app],{stdio:'inherit'});
}
fs.rmSync(entitlementsDir,{recursive:true});
execFileSync('/usr/bin/codesign',['--verify','--deep','--strict',targetApp],{stdio:'inherit'});
console.log('Verified internal desktop build:',targetApp);
