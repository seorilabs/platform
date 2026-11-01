# Web 압축 HTTP 응답 회귀

실제 Identity adapter와 HTTP transport가 같은 출처, 다른 출처의 헤더 노출과 비노출에서 gzip JSON 본문을 읽는지 검사한다. Google과 Platform API는 호출하지 않으며 인증 값은 모두 합성 문자열이다.

같은 Godot 버전으로 내보낸 Web 런타임의 `index.js`, `index.wasm`과 작업 전용 에디터를 준비한다. macOS에서는 원본 서명된 전체 Godot.app 사본의 실행 파일 옆에 `_sc_`를 두고 Android 설정이 없는 별도 editor_data를 사용한다. 기기 조회는 필요 없다.

```bash
python3 sdk-gdscript/addons/seorilabs_platform/tools/http_response_fixture/prepare.py \
  --output /tmp/sdk-http-probe \
  --godot /path/to/portable/Godot.app/Contents/MacOS/Godot \
  --engine-dir /path/to/same-version-web-runtime
```

새 폴더에 현재 SDK 5개 파일과 SHA를 복제하고, native 6건을 실행한 뒤 Web용 PCK와 전용 Edge 실행 파일을 만든다. 서버는 loopback 두 포트만 사용한다. 포트가 쓰이고 있으면 `--same-origin-port`, `--cross-origin-port`로 다른 포트를 지정한다.

출력된 `browserRunner`를 `seorilabs-browser-session`의 자기 세션에서 실행한다. Web 6건, native 6건, Web 오류 0건과 외부 요청 0건을 요구한다. 브라우저 직접 fetch 대조도 압축 헤더와 이미 풀린 본문을 확인한다. `web-result.json`, `native-result.json`, `source-binding.json`이 실제 검증 근거다. 서버는 실행한 프로세스에 SIGTERM 또는 Ctrl-C를 보내 종료한다.

기존 SDK는 브라우저에 Content-Encoding이 보이는 4건에서 이중 해제로 실패하고, 비노출 2건만 통과한다. Web에서만 HTTPRequest의 자동 해제를 끄면 6건이 통과하며 native의 gzip 해제도 유지된다. 이 테스트는 전체 앱 회귀나 실제 마켓 SDK 검수를 대신하지 않는다. 도구와 생성 PCK, 런타임은 정식 SDK 배포본에서 제외한다.
