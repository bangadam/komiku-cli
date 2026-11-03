#!/usr/bin/env python3
"""Solve a Cloudflare challenge with Camoufox and print the browser session.

Usage: solve.py <url>

Prints one JSON object on stdout:
    {"ua": "<user agent>", "cookies": "name=value; ...", "title": "<page title>"}

The cookies (notably cf_clearance) are bound to the IP address and the
User-Agent that solved the challenge, so komiku-cli must send both together.

Camoufox is optional: install it with `python3 -m pip install camoufox`.
"""

import json
import sys
import time

ATTEMPTS = 2
WAIT_SECONDS = 90

CHALLENGE_TITLES = (
    "tunggu sebentar",
    "just a moment",
    "attention required",
    "verifying you are human",
    "checking your browser",
)


def is_challenge(title: str) -> bool:
    lowered = title.strip().lower()
    return any(marker in lowered for marker in CHALLENGE_TITLES)


def attempt(url: str) -> dict:
    from camoufox.sync_api import Camoufox

    with Camoufox(headless=True, humanize=True, geoip=True) as browser:
        page = browser.new_page()
        try:
            page.goto(url, wait_until="commit", timeout=60000)
        except Exception as error:  # navigation can be interrupted by the challenge
            sys.stderr.write(f"navigation: {error}\n")

        deadline = time.time() + WAIT_SECONDS
        title = ""
        while time.time() < deadline:
            time.sleep(1)
            try:
                title = page.title()
            except Exception:
                continue
            if title and not is_challenge(title):
                break

        cookies = page.context.cookies()
        cookie_header = "; ".join(f'{cookie["name"]}={cookie["value"]}' for cookie in cookies)
        user_agent = page.evaluate("navigator.userAgent")
        if not cookie_header:
            raise RuntimeError("the challenge did not set cookies")
        if is_challenge(title):
            raise RuntimeError(f"challenge was not solved (title: {title!r})")
        return {"ua": user_agent, "cookies": cookie_header, "title": title}


def main() -> int:
    if len(sys.argv) != 2:
        sys.stderr.write("usage: solve.py <url>\n")
        return 2
    url = sys.argv[1]
    last_error = None
    for index in range(ATTEMPTS):
        try:
            result = attempt(url)
        except Exception as error:
            last_error = error
            sys.stderr.write(f"attempt {index + 1} failed: {error}\n")
            continue
        json.dump(result, sys.stdout)
        sys.stdout.write("\n")
        return 0
    sys.stderr.write(f"cloudflare challenge was not solved: {last_error}\n")
    return 1


if __name__ == "__main__":
    sys.exit(main())
