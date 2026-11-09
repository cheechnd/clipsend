#!/usr/bin/env python3
"""Correct xtool 1.22.2's per-extension signing, preserving the host Keychain group.

Build-time tooling only. Credentials stay in xtool's existing local store; any
working copies live in a private temporary directory and are removed on exit.
"""
import argparse
import os
from pathlib import Path
import plistlib
import struct
import subprocess
import tempfile

ZSIGN_REVISION = "8c8d88c3b0baeb0931ed00a90f7948df06db9b11"
SCRIPTS = Path(__file__).resolve().parent


def run(args, **kwargs):
    return subprocess.run([str(x) for x in args], check=True, **kwargs)


def profile(bundle):
    result = run(["openssl", "cms", "-verify", "-inform", "DER", "-in",
                  bundle / "embedded.mobileprovision", "-noverify"],
                 stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    return plistlib.loads(result.stdout)


def signer():
    cache = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache")) / "clipsend-build" / ZSIGN_REVISION
    source = cache / "zsign"
    cache.mkdir(parents=True, exist_ok=True)
    if not source.exists():
        run(["git", "clone", "https://github.com/xtool-org/zsign", source])
        run(["git", "-C", source, "checkout", "--detach", ZSIGN_REVISION])
    actual = run(["git", "-C", source, "rev-parse", "HEAD"], stdout=subprocess.PIPE).stdout.decode().strip()
    if actual != ZSIGN_REVISION:
        raise RuntimeError("Unexpected signing-tool revision")
    run(["make", "-j4"], cwd=source / "build/linux")
    objects = sorted(p for p in (source / "build/linux/.build").rglob("*.o") if p.name != "zsign.o")
    output = cache / "sign-bundle"
    run(["g++", "-std=c++11", "-O2", "-I" + str(source / "src"),
         "-I" + str(source / "src/common"), SCRIPTS / "sign-bundle.cpp", *objects,
         "-lssl", "-lcrypto", "-o", output])
    return output


def identity_for(profiles, directory):
    for path in sorted(directory.iterdir()):
        if not path.is_file():
            continue
        try:
            item = plistlib.loads(path.read_bytes())
            cert, key = item["certificate"], item["privateKey"]["data"]
        except (ValueError, KeyError, TypeError, plistlib.InvalidFileException):
            continue
        if isinstance(cert, bytes) and isinstance(key, bytes) and all(cert in p["DeveloperCertificates"] for p in profiles):
            return cert, key
    raise RuntimeError("No existing xtool identity matches every embedded provisioning profile")


def signed_entitlements(bundle):
    info = plistlib.loads((bundle / "Info.plist").read_bytes())
    data = (bundle / info["CFBundleExecutable"]).read_bytes()
    if data[:4] != b"\xcf\xfa\xed\xfe":
        raise RuntimeError("Expected an arm64 Mach-O executable")
    count = struct.unpack_from("<I", data, 16)[0]
    position = 32
    for _ in range(count):
        command, size = struct.unpack_from("<II", data, position)
        if command == 0x1D:
            offset, _ = struct.unpack_from("<II", data, position + 8)
            _, _, slots = struct.unpack_from(">III", data, offset)
            for index in range(slots):
                kind, relative = struct.unpack_from(">II", data, offset + 12 + index * 8)
                if kind == 5:
                    _, length = struct.unpack_from(">II", data, offset + relative)
                    return plistlib.loads(data[offset + relative + 8:offset + relative + length])
        position += size
    raise RuntimeError("Missing signed entitlements")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("app", type=Path)
    parser.add_argument("--signing-store", type=Path, default=Path(os.environ.get("XDG_CONFIG_HOME", Path.home() / ".config")) / "xtool/data")
    args = parser.parse_args()
    app = args.app.resolve()
    bundles = [app, *sorted((app / "PlugIns").glob("*.appex"))]
    if len(bundles) != 2:
        raise RuntimeError("Expected ClipSend and its single Share extension")
    profiles = [profile(b) for b in bundles]
    tool = signer()
    cert, key = identity_for(profiles, args.signing_store)
    # Prefer per-user RAM-backed runtime storage; mkdtemp is private either way.
    runtime = os.environ.get("XDG_RUNTIME_DIR")
    with tempfile.TemporaryDirectory(prefix="clipsend-sign-", dir=runtime) as tmp:
        temporary = Path(tmp)
        cert_path, key_path = temporary / "certificate.der", temporary / "key.pem"
        for path, data in [(cert_path, cert), (key_path, key)]:
            fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            with os.fdopen(fd, "wb") as stream:
                stream.write(data)
        command = [tool, app, cert_path, key_path]
        expected = []
        root_group = None
        for index, (bundle, provision) in enumerate(zip(bundles, profiles)):
            info = plistlib.loads((bundle / "Info.plist").read_bytes())
            granted = provision["Entitlements"]
            team = provision["TeamIdentifier"][0]
            app_id = team + "." + info["CFBundleIdentifier"]
            if granted["application-identifier"] != app_id:
                raise RuntimeError("Provisioning profile does not match bundle")
            if index == 0:
                root_group = app_id
            if root_group not in granted["keychain-access-groups"] and team + ".*" not in granted["keychain-access-groups"]:
                raise RuntimeError("Profile does not permit shared pairing access")
            entitlements = {"application-identifier": app_id,
                            "com.apple.developer.team-identifier": team,
                            "keychain-access-groups": [root_group],
                            "get-task-allow": granted["get-task-allow"]}
            if index == 0:
                entitlements["aps-environment"] = granted["aps-environment"]
            ent_path = temporary / (str(index) + ".plist")
            ent_path.write_bytes(plistlib.dumps(entitlements))
            expected.append(entitlements)
            command.extend([bundle / "embedded.mobileprovision", ent_path])
        run(command)
        for bundle, entitlements in zip(bundles, expected):
            if signed_entitlements(bundle) != entitlements:
                raise RuntimeError("Signed entitlements differ from the expected bundle identity")
    print("Signed ClipSend and Share extension with separate identities and shared pairing access.")


if __name__ == "__main__":
    main()
