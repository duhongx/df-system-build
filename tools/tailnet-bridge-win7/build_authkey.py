#!/usr/bin/env python3
"""Build the Win7 x86 key edition without putting its credential in the repo."""

import argparse
import json
import os
from pathlib import Path
import re
import struct
import subprocess
import tempfile


def main():
    module = Path(__file__).resolve().parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go', required=True, type=Path, help='Go 1.20.14 compiler executable')
    parser.add_argument('--key-file', required=True, type=Path, help='private file containing a one-off Auth Key')
    parser.add_argument('--output', type=Path, default=module.parent.parent / 'output/tailnet-bridge/tailnet-bridge-10.212.11.18-win7-x86-authkey.exe')
    args = parser.parse_args()
    compiler = args.go.expanduser().resolve()
    key = args.key_file.expanduser().read_text().strip()
    if len(key) > 1024 or not re.fullmatch(r'tskey-auth-[A-Za-z0-9_-]+', key):
        parser.error('key file must contain one tskey-auth- enrollment key; credential omitted')
    env = dict(os.environ, GOROOT=str(compiler.parent.parent), CGO_ENABLED='0', GOOS='windows', GOARCH='386', GO386='softfloat')
    version = subprocess.check_output([str(compiler), 'version'], env=env, text=True)
    if not re.search(r'\bgo1\.20\.14\b', version):
        parser.error('this Win7 release requires Go 1.20.14')
    output = args.output.expanduser().resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    # Only the private overlay contains the credential. No -ldflags credential,
    # generated source in the repo, or key file next to the exe is needed.
    with tempfile.TemporaryDirectory(prefix='df-win7-authkey-build-') as directory:
        private = Path(directory)
        bundle = private / 'authkey_bundle.go'
        with bundle.open('x', encoding='utf-8') as stream:
            os.fchmod(stream.fileno(), 0o600)
            stream.write('package main\n\nvar bundledAuthKey = ' + json.dumps(key) + '\nconst authKeyOnly = true\n')
        overlay = private / 'overlay.json'
        overlay.write_text(json.dumps({'Replace': {str(module / 'authkey_bundle.go'): str(bundle)}}))
        # Capture build output, redacting even if a compiler emits source text.
        command = [str(compiler), 'build', '-p', '2', '-trimpath', '-overlay=' + str(overlay), '-ldflags=-s -w', '-o', str(output), '.']
        result = subprocess.run(command, cwd=module, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        if result.returncode:
            raise SystemExit(result.stdout.replace(key, '[REDACTED AUTH KEY]'))
    binary = output.read_bytes()
    pe = struct.unpack_from('<I', binary, 0x3c)[0]
    optional = pe + 24
    assert binary[:2] == b'MZ' and binary[pe:pe + 4] == b'PE\0\0'
    assert struct.unpack_from('<H', binary, pe + 4)[0] == 0x14c
    assert struct.unpack_from('<H', binary, optional)[0] == 0x10b
    assert struct.unpack_from('<HH', binary, optional + 40) == (6, 1)
    assert struct.unpack_from('<HH', binary, optional + 48) == (6, 1)
    assert key.encode() in binary and b'10.212.11.18:22' in binary
    print(f'Built {output.name}: {len(binary)} bytes; PE32 x86; minimum Windows version 6.1')
    print('One-time key embedded; no enrollment request made on this computer.')


if __name__ == '__main__':
    main()
