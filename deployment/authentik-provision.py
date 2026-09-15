"""Create the OAuth provider authentik needs, and print its client id.

Everything here is what README.md's authentik section asks a person to do by
hand with six curl calls. Doing it from a script is not only faster — three of
the fields decide whether a login succeeds and are wrong by default, and a
person copying commands has to know that. They are called out below.

Run twice and nothing is created twice: every step looks for what it is about
to make. The only output on success is the client id, so the caller can read
it into OPENARITY_OIDC_AUDIENCE.
"""

import json
import os
import sys
import urllib.error
import urllib.request

URL = os.environ["AUTHENTIK_URL"].rstrip("/")
TOKEN = os.environ["AUTHENTIK_TOKEN"]


def api(path, body=None, method=None):
    request = urllib.request.Request(
        f"{URL}/api/v3{path}",
        data=json.dumps(body).encode() if body is not None else None,
        method=method or ("POST" if body is not None else "GET"),
        headers={
            "Authorization": f"Bearer {TOKEN}",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            raw = response.read()
            # {} rather than None for an empty body: every caller here reads a
            # field off the result, and a PATCH that answers 204 would
            # otherwise be the one shape that cannot be read.
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as error:
        detail = error.read().decode(errors="replace")[:400]
        raise SystemExit(f"authentik {method or 'GET'} {path} -> {error.code}: {detail}")


def first(path, what):
    results = api(path).get("results", [])
    if not results:
        raise SystemExit(f"authentik has no {what} — it ships one, so this install is not one we wrote")
    return results[0]


def existing(path, field, value):
    for row in api(path).get("results", []):
        if row.get(field) == value:
            return row
    return None


def provider():
    found = existing("/providers/oauth2/", "name", "openarity")
    if found:
        return found

    authorization = first(
        "/flows/instances/?designation=authorization", "authorization flow"
    )
    invalidation = first(
        "/flows/instances/?designation=invalidation", "invalidation flow"
    )
    certificate = first("/crypto/certificatekeypairs/", "signing certificate")

    return api(
        "/providers/oauth2/",
        {
            "name": "openarity",
            "authorization_flow": authorization["pk"],
            # Required, and cannot be null. A provider without it is created
            # and then refuses every login.
            "invalidation_flow": invalidation["pk"],
            # Public: no client secret. A CLI ships its client id to every
            # user, so a secret alongside it is a secret in name only.
            "client_type": "public",
            # Defaults to [], and an empty list refuses every login with
            # "the request is otherwise malformed". The line that explains it
            # is only in authentik's own log. device_code is what `oa login`
            # uses; the other two are the dashboard's.
            "grant_types": [
                "authorization_code",
                "refresh_token",
                "urn:ietf:params:oauth:grant-type:device_code",
            ],
            "signing_key": certificate["pk"],
            # Decides what `sub` contains. The default, hashed_user_id, is an
            # opaque hash — so OPENARITY_SUPER_ADMINS=akadmin would never
            # match and every privileged call would answer 403.
            "sub_mode": "user_username",
            "redirect_uris": [
                {"matching_mode": "strict", "url": "/device/callback"},
                {"matching_mode": "strict", "url": f"{URL}/ui/callback"},
                {"matching_mode": "strict", "url": "http://localhost:8080/callback"},
            ],
        },
    )


def application(provider_pk):
    found = existing("/core/applications/", "slug", "openarity")
    if found:
        return found
    return api(
        "/core/applications/",
        {"name": "Openarity", "slug": "openarity", "provider": provider_pk},
    )


def device_code_flow():
    """The page `oa login` sends someone to, which 404s until this exists.

    Authentik ships no flow with this designation, and the brand points at
    none — so the device flow returns a code, the person opens the address,
    and authentik answers 404 with nothing explaining it.
    """
    brand = first("/core/brands/", "brand")
    if brand.get("flow_device_code"):
        return

    flow = existing("/flows/instances/?designation=stage_configuration", "slug", "device-code")
    if not flow:
        flow = api(
            "/flows/instances/",
            {
                "name": "Device code",
                "title": "Enter the code from your terminal",
                "slug": "device-code",
                "designation": "stage_configuration",
            },
        )

    # brand_uuid, not pk. Flows and certificate keypairs are addressed by pk
    # and a brand is not — its serializer names the field after the model, so
    # a brand read back has no pk at all and this failed with KeyError on the
    # last line of provisioning, after everything else had been created.
    api(
        f"/core/brands/{brand['brand_uuid']}/",
        {"flow_device_code": flow["pk"]},
        method="PATCH",
    )


def main():
    created = provider()
    application(created["pk"])
    device_code_flow()
    print(created["client_id"])


if __name__ == "__main__":
    sys.exit(main())
