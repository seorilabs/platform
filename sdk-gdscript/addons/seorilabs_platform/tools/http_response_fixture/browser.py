"""prepare.py가 실행 폴더를 넣은 뒤 전용 Edge 세션에서 실행한다."""
import datetime
import json
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path('__FIXTURE_ROOT__')


async def main(session):
    config = json.loads((ROOT / 'config.json').read_text())
    tab = await session.new_page()
    page = session.page(tab)
    logs, blocked = [], []

    async def route(request_route):
        url = urlsplit(request_route.request.url)
        if url.hostname in ['127.0.0.1', 'localhost'] or url.scheme in ['data', 'blob']:
            await request_route.continue_()
        else:
            blocked.append(url.scheme + '://' + str(url.hostname))
            await request_route.abort()

    await page.route('**/*', route)
    page.on('console', lambda message: logs.append({'type': message.type, 'text': message.text}))
    page.on('pageerror', lambda error: logs.append({'type': 'pageerror', 'text': str(error)}))
    try:
        await page.goto(config['sameOrigin'], wait_until='domcontentloaded')
        await page.wait_for_function('window.fixtureResult || window.fixtureStartupError', timeout=50000)
        result = await page.evaluate('({report:window.fixtureResult || null,startupError:window.fixtureStartupError || null})')
        control = await page.evaluate("""async (config) => {
            const rows=[];
            for (const exposure of ['same','exposed','hidden']) {
                const base=exposure==='same'?config.sameOrigin:config.crossOrigin;
                const r=await fetch(base+'/gzip/identity?exposure='+exposure, {
                    method:'POST',headers:{'Content-Type':'application/json'},body:'{}'
                });
                const text=await r.text(); const data=JSON.parse(text);
                rows.push({exposure,status:r.status,contentEncoding:r.headers.get('content-encoding'),
                    contentLength:r.headers.get('content-length'),decodedTextBytes:text.length,marker:data.fixtureMarker});
            }
            return rows;
        }""", config)
        output = {'recordedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(), **result,
                  'browserFetchControl': control, 'blockedExternalOrigins': blocked, 'console': logs}
        (ROOT / 'web-result.json').write_text(json.dumps(output, ensure_ascii=False, indent=2) + '\n')
        report = result.get('report') or {}
        assert report.get('passed') == report.get('total') == 6, output
        assert not blocked and not result.get('startupError'), output
        assert not any(item['type'] in ['error', 'pageerror'] for item in logs), output
        return output
    finally:
        await session.close_page(tab)
