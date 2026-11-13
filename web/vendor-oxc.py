#!/usr/bin/env python3
"""Maintenance only: reproduce the pinned npm releases, verifying archive integrity.
Use --output /tmp/oxc-copy to compare a fresh reproduction before replacing a pin.
The normal build never invokes this script or the network.
"""
import argparse
import base64
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import tarfile
import urllib.request

web = Path(__file__).resolve().parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--output', type=Path, default=web / 'vendor/oxc')
output = parser.parse_args().output
pins = json.loads((web / 'vendor/oxc/provenance.json').read_text())
expected = set()
for pin in pins:
    raw = urllib.request.urlopen(pin['url'], timeout=60).read()
    integrity = 'sha512-' + base64.b64encode(hashlib.sha512(raw).digest()).decode()
    if integrity != pin['integrity']:
        raise ValueError('Release checksum mismatch: ' + pin['name'])
    target = output / pin['directory']
    with tarfile.open(fileobj=io.BytesIO(raw), mode='r:gz') as archive:
        for member in archive.getmembers():
            parts = PurePosixPath(member.name).parts
            if parts[0] != 'package' or '..' in parts or member.issym() or member.islnk():
                raise ValueError('Unsafe archive member: ' + member.name)
            path = target.joinpath(*parts[1:])
            if member.isdir():
                path.mkdir(parents=True, exist_ok=True)
            elif member.isfile():
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(archive.extractfile(member).read())
                expected.add(path)
    manifest = target / 'package.json'
    package = json.loads(manifest.read_text())
    if package.get('devDependencies') or package.get('scripts') or pin['name'] == 'oxc-parser':
        original = target / 'package.upstream.json'
        original.write_bytes(manifest.read_bytes())
        expected.add(original)
        package.pop('devDependencies', None)
        package.pop('scripts', None)
        if pin['name'] == 'oxc-parser':
            package['optionalDependencies'] = {key: value for key, value in package['optionalDependencies'].items()
                                               if key in {item['name'] for item in pins}}
        manifest.write_text(json.dumps(package, indent=2) + '\n')
    extra = set(path for path in target.rglob('*') if path.is_file()) - expected
    if extra:
        raise ValueError('Stale files: reproduce into an empty --output directory: ' + str(extra))
(output / 'LICENSE').write_bytes((output / 'oxc-parser/LICENSE').read_bytes())
hashes = {str(path.relative_to(output)): hashlib.sha256(path.read_bytes()).hexdigest() for path in sorted(expected)}
(output / 'sha256.json').write_text(json.dumps(hashes, indent=2) + '\n')
print('Verified and reproduced', len(pins), 'packages;', len(hashes), 'files')
