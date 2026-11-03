#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Validate build digests; publish two-platform indexes and their release notes."""
import argparse
import json
from pathlib import Path
import re
import subprocess

COMPONENTS = ('fxp_core', 'go_gateway', 'simday', 'fix_bridge', 'fxp-replay')
ARCHES = ('amd64', 'arm64')
DIGEST = re.compile(r'sha256:[0-9a-f]{64}')


def digest(value):
    if not isinstance(value, str) or not DIGEST.fullmatch(value):
        raise ValueError('invalid image digest')
    return value


def read_json(path):
    if path.is_symlink() or path.stat().st_size > 1024 * 1024:
        raise ValueError('unexpected digest artifact size/type')
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('duplicate digest artifact key')
            result[key] = value
        return result
    return json.loads(path.read_text(), object_pairs_hook=unique)


def records(directory):
    result = {}
    for arch in ARCHES:
        record = read_json(directory / f'{arch}.json')
        if record['architecture'] != arch or set(record['images']) != set(COMPONENTS):
            raise ValueError('incomplete architecture/component digest set')
        result[arch] = {name: digest(record['images'][name]) for name in COMPONENTS}
    return result


def coordinates(prefix, tag, sha):
    if not re.fullmatch(r'ghcr\.io/[a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_.-]*', prefix):
        raise ValueError('expected lowercase ghcr.io/owner/repository prefix')
    if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?', tag):
        raise ValueError('expected a vMAJOR.MINOR.PATCH tag')
    if not re.fullmatch(r'[0-9a-f]{40}', sha):
        raise ValueError('expected full Git SHA')


def publish(directory, prefix, tag, sha, notes, lock):
    coordinates(prefix, tag, sha)
    built = records(directory)  # Validate ALL artifacts before any mutation.
    result = {'version': tag, 'git_sha': sha, 'images': {}}
    lines = ['\n## Container images\n', f'Source revision: `{sha}`. Tags: `{tag}` and `{sha}`.\n',
             'Use the immutable **index** references below; each contains Linux amd64 and arm64.\n',
             '| Component | Immutable image | linux/amd64 digest | linux/arm64 digest |',
             '| --- | --- | --- | --- |']
    for name in COMPONENTS:
        image = f'{prefix}/{name}'
        subprocess.run(['docker', 'buildx', 'imagetools', 'create', '-t', f'{image}:{tag}',
                        '-t', f'{image}:{sha}',
                        *[f'{image}@{built[arch][name]}' for arch in ARCHES]], check=True)
        # Buildx's top-level Digest line identifies the published index, not the
        # image config ID or one architecture's manifest.
        description = subprocess.check_output(
            ['docker', 'buildx', 'imagetools', 'inspect', f'{image}:{tag}'], text=True)
        matches = re.findall(r'^Digest:\s+(sha256:[0-9a-f]{64})\s*$', description, re.M)
        if len(matches) != 1:
            raise ValueError('published index digest unavailable')
        index = digest(matches[0])
        raw = subprocess.check_output(['docker', 'buildx', 'imagetools', 'inspect', '--raw',
                                       f'{image}@{index}'], text=True)
        manifests = json.loads(raw)['manifests']
        actual = {(m['platform']['os'], m['platform']['architecture']): digest(m['digest'])
                  for m in manifests}
        expected = {('linux', arch): built[arch][name] for arch in ARCHES}
        if len(manifests) != 2 or actual != expected:
            raise ValueError('published platforms/digests differ from build outputs')
        result['images'][name] = {'image': f'{image}@{index}', 'digest': index,
                                 'platforms': {f'linux/{a}': built[a][name] for a in ARCHES}}
        lines.append(f'| {name} | `{image}@{index}` | `{built["amd64"][name]}` | `{built["arm64"][name]}` |')
    notes.write_text('\n'.join(lines) + '\n')
    lock.write_text(json.dumps(result, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    record = commands.add_parser('record')
    record.add_argument('--arch', choices=ARCHES, required=True)
    record.add_argument('--metadata-dir', type=Path, required=True)
    record.add_argument('--output', type=Path, required=True)
    combine = commands.add_parser('publish')
    combine.add_argument('--directory', type=Path, required=True)
    combine.add_argument('--prefix', required=True)
    combine.add_argument('--tag', required=True)
    combine.add_argument('--sha', required=True)
    combine.add_argument('--notes', type=Path, required=True)
    combine.add_argument('--lock', type=Path, required=True)
    args = parser.parse_args()
    if args.command == 'record':
        images = {name: digest(read_json(args.metadata_dir / f'{name}.json')['containerimage.digest'])
                  for name in COMPONENTS}
        args.output.write_text(json.dumps({'architecture': args.arch, 'images': images}, indent=2) + '\n')
    else:
        publish(args.directory, args.prefix, args.tag, args.sha, args.notes, args.lock)


if __name__ == '__main__':
    main()
