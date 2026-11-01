#!/usr/bin/env python3
"""현재 SDK로 합성 gzip HTTP 테스트를 준비하고 로컬 서버를 유지한다."""
import argparse
import datetime
import gzip
import hashlib
import json
import mimetypes
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit


def main():
    sources = Path(__file__).resolve().parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--godot', type=Path, required=True)
    parser.add_argument('--engine-dir', type=Path, required=True)
    parser.add_argument('--same-origin-port', type=int, default=8766)
    parser.add_argument('--cross-origin-port', type=int, default=8767)
    args = parser.parse_args()
    if args.same_origin_port == args.cross_origin_port:
        parser.error('same-origin과 cross-origin은 다른 포트여야 한다')
    godot = args.godot.expanduser().resolve()
    if sys.platform == 'darwin' and not (godot.parent / '_sc_').is_file():
        parser.error('공유 에디터 설정 대신 _sc_가 있는 작업 전용 Godot.app 사본을 사용해야 한다')
    engine = args.engine_dir.expanduser().resolve()
    for name in ['index.js', 'index.wasm']:
        if not (engine / name).is_file():
            parser.error('engine-dir에 같은 버전의 ' + name + ' 파일이 필요하다')
    root = args.output.expanduser().resolve()
    root.mkdir(parents=True, exist_ok=False)
    project, web = root / 'project', root / 'web'
    project.mkdir(); web.mkdir()
    config = {'sameOrigin': 'http://127.0.0.1:' + str(args.same_origin_port),
              'crossOrigin': 'http://127.0.0.1:' + str(args.cross_origin_port)}
    (root / 'config.json').write_text(json.dumps(config) + '\n')
    addon = sources.parents[1]
    binding = []
    for name in ['adapters/firebase_identity_adapter.gd', 'core/http_transport.gd',
                 'core/atomic_json_store.gd', 'core/backoff.gd', 'core/envelope.gd']:
        data = (addon / name).read_bytes()
        destination = project / 'addons/seorilabs_platform' / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(data)
        binding.append({'path': name, 'sha256': hashlib.sha256(data).hexdigest()})
    (root / 'source-binding.json').write_text(json.dumps(binding, indent=2) + '\n')
    for name in ['main.gd', 'main.tscn']:
        (project / name).write_bytes((sources / name).read_bytes())
    (web / 'index.html').write_bytes((sources / 'index.html').read_bytes())
    (root / 'browser-run.py').write_text((sources / 'browser.py').read_text().replace(
        "Path('__FIXTURE_ROOT__')", 'Path(' + repr(str(root)) + ')'))
    (project / 'project.godot').write_text('config_version=5\n[application]\n'
        'config/name="SDK HTTP fixture"\nrun/main_scene="res://main.tscn"\n'
        '[rendering]\nrenderer/rendering_method="gl_compatibility"\n[test]\n'
        'same_origin=' + json.dumps(config['sameOrigin']) + '\n'
        'cross_origin=' + json.dumps(config['crossOrigin']) + '\n')
    (project / 'export_presets.cfg').write_text('[preset.0]\nname="Web"\nplatform="Web"\n'
        'runnable=true\nexport_filter="all_resources"\ninclude_filter=""\nexclude_filter=""\n'
        'export_path="../web/fixture.pck"\nscript_export_mode=0\n'
        '[preset.0.options]\nvariant/thread_support=false\nvariant/extensions_support=false\n'
        'vram_texture_compression/for_desktop=true\n')

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_OPTIONS(self):
            self.send_response(204)
            self.send_header('Access-Control-Allow-Origin', config['sameOrigin'])
            self.send_header('Access-Control-Allow-Methods', 'GET, POST, OPTIONS')
            self.send_header('Access-Control-Allow-Headers', 'Content-Type, X-Seori-App, X-Seori-Build')
            self.send_header('Content-Length', '0')
            self.end_headers()

        def do_POST(self):
            self.process()

        def do_GET(self):
            self.process()

        def process(self):
            url = urlsplit(self.path)
            exposure = parse_qs(url.query).get('exposure', [''])[0]
            if self.command == 'POST':
                self.rfile.read(int(self.headers.get('Content-Length', '0')))
            if url.path.startswith('/gzip/'):
                identity = {'idToken': 'synthetic-id-token-not-a-credential',
                            'refreshToken': 'synthetic-refresh-token-not-a-credential',
                            'localId': 'synthetic-user', 'expiresIn': '3600',
                            'fixtureMarker': 'gzip_fixture_v1', 'padding': 'synthetic-content-' * 80}
                payload = identity if url.path.endswith('/identity') else {
                    'ok': True, 'result': {'fixtureMarker': 'gzip_fixture_v1', 'padding': 'synthetic-content-' * 80}}
                plain = json.dumps(payload, separators=(',', ':')).encode()
                data = gzip.compress(plain, mtime=0)
                self.send_response(200)
                self.send_header('Content-Type', 'application/json; charset=UTF-8')
                self.send_header('Content-Encoding', 'gzip')
                self.send_header('Content-Length', str(len(data)))
                self.send_header('Access-Control-Allow-Origin', config['sameOrigin'])
                self.send_header('Cross-Origin-Resource-Policy', 'cross-origin')
                if exposure == 'exposed':
                    self.send_header('Access-Control-Expose-Headers', 'Content-Encoding, Content-Length')
                self.end_headers()
                self.wfile.write(data)
                record = {'at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'path': url.path,
                          'exposure': exposure, 'port': self.server.server_address[1],
                          'acceptEncoding': self.headers.get('Accept-Encoding', ''),
                          'gzipBytes': len(data), 'decodedBytes': len(plain)}
                with (root / 'requests.jsonl').open('a') as file:
                    file.write(json.dumps(record) + '\n')
                return
            file = engine / Path(url.path).name if url.path.startswith('/engine/') else (
                web / ('index.html' if url.path == '/' else url.path.lstrip('/'))).resolve()
            if not file.is_file() or not (file.is_relative_to(engine) or file.is_relative_to(web)):
                self.send_error(404)
                return
            data = file.read_bytes()
            self.send_response(200)
            self.send_header('Content-Type', 'application/wasm' if file.suffix == '.wasm' else
                             mimetypes.guess_type(file.name)[0] or 'application/octet-stream')
            self.send_header('Content-Length', str(len(data)))
            self.send_header('Cross-Origin-Opener-Policy', 'same-origin')
            self.send_header('Cross-Origin-Embedder-Policy', 'require-corp')
            self.send_header('Cross-Origin-Resource-Policy', 'cross-origin')
            self.end_headers()
            self.wfile.write(data)

    servers = []
    try:
        for port in [args.same_origin_port, args.cross_origin_port]:
            server = ThreadingHTTPServer(('127.0.0.1', port), Handler)
            servers.append(server)
            threading.Thread(target=server.serve_forever, daemon=True).start()
        environment = os.environ.copy()
        for name in ['ANDROID_HOME', 'ANDROID_SDK_ROOT', 'JAVA_HOME']:
            environment.pop(name, None)

        def run(name, arguments):
            process = subprocess.run([str(godot), '--headless', '--path', str(project), *arguments],
                                     env=environment, text=True, capture_output=True, timeout=120)
            log = process.stdout + process.stderr
            (root / (name + '.log')).write_text(log)
            if process.returncode or re.search(r'SCRIPT ERROR|ERROR:', log):
                raise RuntimeError(name + ' 실패: ' + str(root / (name + '.log')))
            return log

        run('import', ['--editor', '--import', '--quit'])
        native = run('native', [])
        marker = 'GZIP_FIXTURE_RESULT '
        report = json.loads(next(line.split(marker, 1)[1] for line in native.splitlines() if marker in line))
        (root / 'native-result.json').write_text(json.dumps(report, indent=2) + '\n')
        assert report['passed'] == report['total'] == 6, report
        run('export', ['--export-pack', 'Web', str(web / 'fixture.pck')])
        print(json.dumps({'ready': True, 'nativePassed': 6, 'browserRunner': str(root / 'browser-run.py'),
                          'sameOrigin': config['sameOrigin'], 'realFirebaseCalls': 0}), flush=True)
        stop = threading.Event()
        signal.signal(signal.SIGTERM, lambda *_args: stop.set())
        signal.signal(signal.SIGINT, lambda *_args: stop.set())
        stop.wait()
    finally:
        for server in servers:
            server.shutdown()
            server.server_close()


if __name__ == '__main__':
    main()
