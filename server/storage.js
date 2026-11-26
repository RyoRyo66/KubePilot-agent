import { constants, closeSync, existsSync, fstatSync, lstatSync, mkdirSync, openSync,
  readFileSync, realpathSync, writeFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { join, relative, resolve } from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { randomBytes } from 'node:crypto';
import { ensure } from './common.js';

const windows = process.platform === 'win32';
const nofollow = constants.O_NOFOLLOW || 0;
function check(stat, dir) {
  ensure(stat && !stat.isSymbolicLink() && (dir ? stat.isDirectory() : stat.isFile() && stat.nlink === 1),
    '数据目录含有链接或异常文件，请离线检查');
  if (!windows) ensure(stat.uid === process.getuid() && !(stat.mode & 0o077),
    '数据必须由服务用户独占，请离线检查属主和权限');
}
// Node's POSIX mode bits do not express a Windows DACL. Provision an empty
// directory before creating secrets; existing nonempty stores are verification-only.
const aclScript = [
  "$ErrorActionPreference='Stop'",
  "$root=$env:KUBEPILOT_ACL_PATH",
  "$me=[Security.Principal.WindowsIdentity]::GetCurrent().User",
  "$sid=[Security.Principal.SecurityIdentifier]",
  "$allowed=@($me.Value,'S-1-5-18','S-1-5-32-544')",
  "$acl=[IO.Directory]::GetAccessControl($root)",
  "if ($allowed -notcontains $acl.GetOwner($sid).Value) {throw 'Unexpected directory owner'}",
  "if (-not [IO.Directory]::EnumerateFileSystemEntries($root).GetEnumerator().MoveNext()) {",
  " $acl.SetAccessRuleProtection($true,$false)",
  " foreach ($rule in @($acl.GetAccessRules($true,$false,$sid))) {$acl.RemoveAccessRuleSpecific($rule)}",
  " foreach($id in $allowed) {$acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new($id),'FullControl','ContainerInherit,ObjectInherit','None','Allow'))}",
  " [IO.Directory]::SetAccessControl($root,$acl)",
  "}",
  "$paths=@($root) + @($env:KUBEPILOT_ACL_FILES.Split(',') | ForEach-Object {Join-Path $root $_} | Where-Object {[IO.File]::Exists($_)})",
  "foreach ($p in $paths) {",
  " $a=if($p -eq $root){[IO.Directory]::GetAccessControl($p)}else{[IO.File]::GetAccessControl($p)}",
  " if ($allowed -notcontains $a.GetOwner($sid).Value) {throw 'Unexpected file owner'}",
  " foreach ($r in $a.GetAccessRules($true,$true,$sid)) {if ($r.AccessControlType -eq 'Allow' -and $allowed -notcontains $r.IdentityReference.Value) {throw 'Data ACL is not private'}}",
  "}",
].join('\n');

export function privateRoot(dir, files) {
  dir = resolve(dir);
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  check(lstatSync(dir), true);
  ensure(relative(dir, realpathSync.native(dir)) === '', '数据目录及其上级不能是链接');
  for (const name of files) {
    const st = lstatSync(join(dir, name), { throwIfNoEntry: false });
    if (st) check(st, false);
  }
  if (windows) execFileSync(join(process.env.SystemRoot, 'System32/WindowsPowerShell/v1.0/powershell.exe'), ['-NoProfile','-NonInteractive','-Command',aclScript],
    { windowsHide: true, env: { ...process.env, KUBEPILOT_ACL_PATH: dir, KUBEPILOT_ACL_FILES: files.join(',') } });
  return dir;
}
export const sqliteFiles = name => [name, name + '-wal', name + '-shm', name + '-journal'];
function checkedOpen(path, flags) {
  const fd = openSync(path, flags | nofollow | (constants.O_NONBLOCK || 0), 0o600);
  try {
    const stat = fstatSync(fd);
    check(stat, false);
    const named = lstatSync(path);
    check(named, false);
    ensure(stat.dev === named.dev && stat.ino === named.ino, '数据文件已被替换');
    return fd;
  } catch (error) { closeSync(fd); throw error; }
}
export function sqlite(dir, name) {
  const path = join(dir, name);
  const fd = checkedOpen(path, constants.O_RDWR | constants.O_CREAT);
  try { return new DatabaseSync(path); } finally { closeSync(fd); }
}
export function storageKey(dir) {
  const path = join(dir, 'storage.key');
  if (!existsSync(path)) {
    ensure(!existsSync(join(dir, 'kubepilot.db')), '数据密钥缺失，需要恢复备份');
    const fd = checkedOpen(path, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL);
    try { writeFileSync(fd, randomBytes(32)); } finally { closeSync(fd); }
  }
  const fd = checkedOpen(path, constants.O_RDONLY);
  try {
    ensure(fstatSync(fd).size === 32, '数据密钥损坏');
    return readFileSync(fd);
  } finally { closeSync(fd); }
}
