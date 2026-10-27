#!/usr/bin/env python3
"""Create a local remote without putting its password in process arguments."""
import argparse
import ipaddress
import json
import os
import pathlib
import stat
import re
import tempfile
import subprocess
import sys
import urllib.parse

parser = argparse.ArgumentParser()
parser.add_argument('--remote-name', default='idrive-local')
parser.add_argument('--url', default='http://127.0.0.1:8787')
parser.add_argument('--user', default='idrive')
parser.add_argument('--password-file')
parser.add_argument('--config')
args = parser.parse_args()
url = urllib.parse.urlsplit(args.url)
try:
    local = url.hostname == 'localhost' or ipaddress.ip_address(url.hostname).is_loopback
except ValueError:
    local = False
if not local or url.scheme not in ('http', 'https') or url.username is not None or url.password is not None:
    sys.exit('Use an HTTP(S) loopback gateway URL without embedded credentials.')
if not re.fullmatch(r'[A-Za-z0-9_-]+', args.remote_name):
    sys.exit('Invalid rclone remote name.')
if args.password_file:
    if not stat.S_ISREG(os.stat(args.password_file).st_mode):
        sys.exit('Password file must be a regular file.')
    with open(args.password_file, 'rb') as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or (os.name != 'nt' and stat.S_IMODE(info.st_mode) & 0o077):
            sys.exit('Password file must be a private regular file (0600 on Unix).')
        data = source.read(4097)
else:
    data = sys.stdin.buffer.read(4097)
if len(data) > 4096:
    sys.exit('Password is too long.')
password = data.decode('utf-8').removesuffix('\n').removesuffix('\r')
if not password or '\r' in password or '\n' in password:
    sys.exit('Password must be a single nonempty line.')
if args.user != args.user.strip() or ':' in args.user or '\0' in args.user:
    sys.exit('Bridge username cannot contain a colon, NUL or surrounding whitespace.')
if any(c in args.url + args.user for c in '\r\n'):
    sys.exit('URL and user must each contain one line.')
fallback = pathlib.Path.home() / ('Library/Application Support' if sys.platform == 'darwin' else '.config')
base = pathlib.Path(os.environ.get('APPDATA') or os.environ.get('XDG_CONFIG_HOME') or fallback)
config = pathlib.Path(args.config) if args.config else base / 'idrive-gateway' / ('rclone-' + args.remote_name + '.conf')
if config.exists():
    with config.open() as existing:
        content = existing.read(65537)
    if len(content) > 65536:
        sys.exit('Config is too large for a dedicated helper file; choose another --config path.')
    sections = re.findall(r'^\[([^]\r\n]+)\]\s*$', content, re.M)
    if sections != [args.remote_name]:
        sys.exit('Refusing to replace a config containing other remotes or encrypted data; choose a dedicated --config path.')
result = subprocess.run([os.environ.get('IDRIVE_RCLONE_BINARY','rclone'), 'obscure', '-'], input=(password+'\n').encode(), capture_output=True, env=dict(os.environ,RCLONE_CONFIG=('NUL' if os.name=='nt' else os.devnull)),timeout=30)
if result.returncode:
    sys.exit('rclone password obscuring failed.')
obscured = result.stdout.decode().strip()
if not obscured or '\n' in obscured or '\r' in obscured:
    sys.exit('Invalid rclone obscuring result.')
config.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
fd, temporary = tempfile.mkstemp(prefix='.rclone-', dir=config.parent)
try:
    with os.fdopen(fd, 'w', encoding='utf-8', newline='\n') as output:
        if os.name == 'nt':
            system = pathlib.Path(os.environ['SystemRoot']) / 'System32'
            identity = subprocess.run([str(system/'whoami.exe'), '/user', '/fo', 'csv', '/nh'], capture_output=True, check=True)
            sid = re.search(rb'S-1-(?:[0-9]+-)*[0-9]+', identity.stdout)
            if not sid:
                sys.exit('Cannot determine the current user for private config permissions.')
            subprocess.run([str(system/'icacls.exe'), temporary, '/inheritance:r', '/grant:r', '*'+sid[0].decode()+':F', '/q'], capture_output=True, check=True)
        output.write(f'[{args.remote_name}]\ntype = webdav\nurl = {args.url}\nvendor = other\nencoding = Slash,Dot,Ctl,Del,RightSpace,RightCrLfHtVt,InvalidUtf8\nuser = {args.user}\npass = {obscured}\n')
        output.flush()
        os.fsync(output.fileno())
    os.replace(temporary, config)
    if os.name != 'nt':
        directory = os.open(config.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
finally:
    pathlib.Path(temporary).unlink(missing_ok=True)
print(f'Configured private remote {args.remote_name!r} in {str(config)!r}.')
print('Pass this file via --config to rclone or idrive-rclone. See docs/verified-mount.md.')
