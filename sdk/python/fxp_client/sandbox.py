# SPDX-License-Identifier: MIT
"""FXP Sandbox helper: API key -> run -> fresh short-lived run token -> Client.

The workspace API key is only sent to the sandbox control plane over verified
TLS; the trading gateway only ever receives a per-run token minted moments
before each connection attempt.
"""
from __future__ import annotations

import json
import os
import ssl
import time
import urllib.error
import urllib.request

from .client import Client

LIVE_STATES = ('requested', 'starting', 'ready', 'running')
# OSError covers refused/reset/unreachable/timeouts and TLS EOF; certificate
# failures are re-raised before this tuple is consulted.
_RETRYABLE = (OSError, EOFError)


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """Never follow redirects, so the API key is never sent anywhere else."""
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _opener(tls: ssl.SSLContext) -> urllib.request.OpenerDirector:
    return urllib.request.build_opener(_NoRedirect, urllib.request.HTTPSHandler(context=tls))


class SandboxError(RuntimeError):
    """A control-plane request failed. Never carries the API key."""
    def __init__(self, status: int, code: str):
        super().__init__(f'sandbox request failed: {status} {code}')
        self.status, self.code = status, code


class Sandbox:
    def __init__(self, url: str | None = None, api_key: str | None = None, *,
                 ca_file: str | None = None, timeout: float = 10):
        self.url = (url or os.environ.get('FXP_URL') or 'https://localhost:8443').rstrip('/')
        if not self.url.startswith('https://'):
            raise ValueError('sandbox URL must use https')
        key = api_key or os.environ.get('FXP_API_KEY')
        if not key:
            raise ValueError('set FXP_API_KEY or pass api_key')
        self.__key = key
        ca_file = ca_file or os.environ.get('FXP_CA_FILE') or ('fxp-local-ca.crt' if os.path.exists('fxp-local-ca.crt') else None)
        self.tls = ssl.create_default_context(cafile=ca_file)
        self._open = _opener(self.tls).open
        self.timeout = timeout

    def __repr__(self) -> str:
        return f'Sandbox(url={self.url!r})'

    def _request(self, method: str, path: str, body: dict | None = None) -> dict:
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.url + path, data=data, method=method, headers={
            'Authorization': 'Bearer ' + self.__key, 'Content-Type': 'application/json'})
        try:
            with self._open(request, timeout=self.timeout) as response:
                raw = response.read(1 << 20)
        except urllib.error.HTTPError as error:
            code = 'request_failed'
            try:
                code = str(json.loads(error.read(4096)).get('error', code))[:64]
            except (ValueError, AttributeError):
                pass
            raise SandboxError(error.code, code) from None
        return json.loads(raw) if raw else {}

    def create_run(self, template_id: str = 'quiet-book', seed: int = 7, *, name: str | None = None,
                   duration_seconds: int | None = None) -> dict:
        body: dict = {'template_id': template_id, 'seed': seed}
        if name is not None:
            body['name'] = name
        if duration_seconds is not None:
            body['duration_seconds'] = duration_seconds
        return self._request('POST', '/v1/runs', body)

    def run(self, run_id: str) -> dict:
        return self._request('GET', '/v1/runs/' + run_id)

    def stop(self, run_id: str) -> dict:
        return self._request('POST', f'/v1/runs/{run_id}/stop')

    def token(self, run_id: str, bot: str) -> str:
        return self._request('POST', f'/v1/runs/{run_id}/token', {'bot_name': bot})['token']

    def wait_until_running(self, run_id: str, *, timeout: float = 180, interval: float = 2) -> dict:
        """Return the run's connection details once it is running."""
        deadline = time.monotonic() + timeout
        while True:
            run = self.run(run_id)
            if run.get('state') == 'running' and run.get('connection'):
                return run['connection']
            if run.get('state') not in LIVE_STATES:
                raise RuntimeError(f"run is {run.get('state')}; start a new run")
            if time.monotonic() + interval > deadline:
                raise TimeoutError('run did not start in time')
            time.sleep(interval)

    def connect(self, run_id: str, bot: str, *, attempts: int = 3, wait: float = 180,
                timeout: float = 5, **client_options) -> Client:
        """Wait for the run, then log on with a freshly minted token per attempt.

        Only connection/logon is retried; certificate failures never are.
        Call again after a disconnect to reconnect with a new token.
        """
        connection = self.wait_until_running(run_id, timeout=wait)
        for attempt in range(attempts):
            token = self.token(run_id, bot)
            try:
                return Client.connect(connection['host'], connection['port'], token, timeout=timeout,
                                      tls=self.tls, server_hostname=connection['server_name'], **client_options)
            except ssl.SSLCertVerificationError:
                raise
            except _RETRYABLE:
                if attempt == attempts - 1:
                    raise
                time.sleep(1)
        raise ValueError('attempts must be positive')


def connect(run_id: str | None = None, bot: str | None = None, *, url: str | None = None,
            api_key: str | None = None, ca_file: str | None = None, **options) -> Client:
    """`fxp_client.sandbox.connect()` using FXP_URL, FXP_API_KEY, FXP_RUN_ID and FXP_BOT."""
    run_id = run_id or os.environ.get('FXP_RUN_ID')
    if not run_id:
        raise ValueError('set FXP_RUN_ID or pass run_id')
    return Sandbox(url, api_key, ca_file=ca_file).connect(run_id, bot or os.environ.get('FXP_BOT') or 'my-bot', **options)
