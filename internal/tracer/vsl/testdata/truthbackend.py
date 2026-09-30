"""Traffic-shaping backend for VSL truth-fixture recording. Endpoints:
/plain            -> 200 "hello"
/slow             -> 200 after a 2s sleep (coalescing window)
/shortttl         -> 200 with Cache-Control: max-age=1 (grace/bgfetch)
/esi              -> 200 ESI markup + Surrogate-Control: content="ESI/1.0"
/frag             -> 200 "FRAGMENT"
/flaky            -> 500 on the first call, 200 afterwards (retry)
/teapot           -> 418 (restart trigger for the rig VCL)
anything else     -> 200 with the path echoed
"""
import http.server
import threading
import time

hits = {}
lock = threading.Lock()


class H(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):  # noqa: N802 - stdlib API name
        with lock:
            n = hits.get(self.path, 0) + 1
            hits[self.path] = n
        body = b"hello"
        status, headers = 200, {}
        if self.path.startswith("/slow"):
            time.sleep(2)
        elif self.path.startswith("/shortttl"):
            headers["Cache-Control"] = "max-age=1"
        elif self.path.startswith("/esi"):
            body = b'before <esi:include src="/frag"/> after'
            headers["Surrogate-Control"] = 'content="ESI/1.0"'
        elif self.path.startswith("/frag"):
            body = b"FRAGMENT"
        elif self.path.startswith("/flaky"):
            if n == 1:
                status, body = 500, b"boom"
        elif self.path.startswith("/teapot"):
            status, body = 418, b"teapot"
        else:
            body = self.path.encode()
        self.send_response(status)
        for k, v in headers.items():
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):  # keep container logs quiet
        pass


http.server.ThreadingHTTPServer(("", 8000), H).serve_forever()
