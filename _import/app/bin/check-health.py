#!/usr/bin/env python3
import argparse
import sys
import time
import urllib.request

BASE_URL = "https://riseriscvrunnerappqdvknz9s-ghfe.functions.fnc.fr-par.scw.cloud"
STAGING_URL = "https://riseriscvrunnerappst73ndwr0w-ghfe.functions.fnc.fr-par.scw.cloud"

MAX_RETRIES = 10
RETRY_DELAY = 5

parser = argparse.ArgumentParser()
parser.add_argument("--staging", action="store_true")
args = parser.parse_args()

url = f"{STAGING_URL if args.staging else BASE_URL}/health"

for i in range(1, MAX_RETRIES + 1):
    try:
        resp = urllib.request.urlopen(url, timeout=10)
        if resp.status == 200:
            print(f"Health check passed (attempt {i}/{MAX_RETRIES})")
            sys.exit(0)
        code = resp.status
    except Exception as e:
        code = str(e)
    print(f"Attempt {i}/{MAX_RETRIES}: {code} - retrying in {RETRY_DELAY}s...")
    time.sleep(RETRY_DELAY)

print(f"Health check failed after {MAX_RETRIES} attempts")
sys.exit(1)
