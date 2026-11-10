"""Mirror the macOS assets from a published Braindex release to Homebrew."""
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

TAP = "sam-bretz/homebrew-tap"
FORMULA = "Formula/braindex.rb"
ARCHES = ("arm64", "amd64")
DOWNLOAD = f"https://github.com/{TAP}/releases/download"


def version_key(tag):
    match = re.fullmatch(
        r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
        r"(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?",
        tag,
    )
    if not match:
        raise ValueError("Use a Braindex tag such as v0.1.0 or v0.1.0-rc.5.")
    major, minor, patch, prerelease = match.groups()
    identifiers = []
    for part in prerelease.split(".") if prerelease else []:
        if part.isdigit() and len(part) > 1 and part.startswith("0"):
            raise ValueError("Numeric prerelease identifiers cannot have leading zeros.")
        identifiers.append((0, int(part)) if part.isdigit() else (1, part))
    return int(major), int(minor), int(patch), prerelease is None, identifiers


def gh(token, *args, payload=None):
    env = os.environ.copy()
    env.pop("SOURCE_TOKEN", None)
    env.pop("TAP_TOKEN", None)
    env["GH_TOKEN"] = token
    result = subprocess.run(
        ["gh", *args],
        env=env,
        input=json.dumps(payload) if payload is not None else None,
        stdout=subprocess.PIPE,
        text=True,
        check=True,
    )
    return result.stdout


def archive_name(tag, arch):
    return f"braindex_{tag}_darwin_{arch}.tar.gz"


def verify_archives(directory, tag):
    manifest = directory / f"braindex_{tag}_checksums.txt"
    hashes = {}
    for arch in ARCHES:
        name = archive_name(tag, arch)
        matches = re.findall(rf"^([0-9a-f]{{64}})  {re.escape(name)}$", manifest.read_text(), re.M)
        with (directory / name).open("rb") as archive:
            digest = hashlib.file_digest(archive, "sha256").hexdigest()
        if matches != [digest]:
            raise ValueError(f"Checksum manifest does not match {name}.")
        hashes[name] = digest
    return hashes


def render_formula(formula, current, tag, hashes):
    formula = formula.replace(f'  version "{current[1:]}"\n', f'  version "{tag[1:]}"\n')
    for arch in ARCHES:
        old_url = f"{DOWNLOAD}/braindex-{current}/{archive_name(current, arch)}"
        name = archive_name(tag, arch)
        pattern = rf'    url "{re.escape(old_url)}"\n    sha256 "[0-9a-f]{{64}}"'
        replacement = f'    url "{DOWNLOAD}/braindex-{tag}/{name}"\n    sha256 "{hashes[name]}"'
        formula, count = re.subn(pattern, replacement, formula)
        if count != 1:
            raise ValueError(f"Expected exactly one {arch} archive entry in {FORMULA}.")
    return formula


def tap_release(token, public_tag):
    # Listing with the tap credential includes drafts left by an interrupted run.
    releases = gh(
        token, "api", "--paginate", f"repos/{TAP}/releases?per_page=100",
        "--jq", f'.[] | select(.tag_name == "{public_tag}")',
    )
    return json.loads(releases) if releases.strip() else None


def check_public_assets(release, hashes):
    assets = {asset["name"]: asset for asset in release["assets"]}
    if assets.keys() - hashes.keys():
        raise ValueError("The public release contains unexpected assets.")
    for name, asset in assets.items():
        if asset["state"] != "uploaded" or asset["digest"] != f"sha256:{hashes[name]}":
            raise ValueError(f"The existing public asset {name} does not match the verified archive.")
    return hashes.keys() - assets.keys()


def mirror(tag, source, source_token, tap_token):
    version = version_key(tag)
    formula_endpoint = f"repos/{TAP}/contents/{FORMULA}"
    file = json.loads(gh(tap_token, "api", f"{formula_endpoint}?ref=main"))
    formula = base64.b64decode(file["content"]).decode()
    versions = re.findall(r'^  version "([^"]+)"$', formula, re.M)
    if len(versions) != 1:
        raise ValueError(f"Expected exactly one version in {FORMULA}.")
    current = f"v{versions[0]}"
    if version < version_key(current):
        print(f"The tap already pins {current}. Skipping older release {tag}.")
        return

    private = json.loads(gh(source_token, "release", "view", tag, "-R", source,
                            "--json", "isDraft,isPrerelease"))
    if private["isDraft"]:
        raise ValueError("Publish the private release before mirroring it.")

    public_tag = f"braindex-{tag}"
    with tempfile.TemporaryDirectory() as temp:
        directory = Path(temp)
        names = [archive_name(tag, arch) for arch in ARCHES]
        manifest = f"braindex_{tag}_checksums.txt"
        patterns = [arg for name in [*names, manifest] for arg in ("--pattern", name)]
        gh(source_token, "release", "download", tag, "-R", source, "-D", temp, *patterns)
        hashes = verify_archives(directory, tag)
        updated = render_formula(formula, current, tag, hashes)
        if current == tag and formula != updated:
            raise ValueError(f"The tap already pins different checksums for {tag}. Use a new tag.")

        release = tap_release(tap_token, public_tag)
        if release is None:
            flags = ["--prerelease"] if private["isPrerelease"] else []
            gh(tap_token, "release", "create", public_tag, "-R", TAP, "--draft",
               "--title", f"Braindex {tag}", "--notes", "Braindex binaries for macOS.",
               "--latest=false", *flags)
            release = tap_release(tap_token, public_tag)

        missing = check_public_assets(release, hashes)
        if missing:
            if not release["draft"]:
                raise ValueError("An existing public release is missing expected assets.")
            gh(tap_token, "release", "upload", public_tag, "-R", TAP,
               *(str(directory / name) for name in sorted(missing)))
            release = tap_release(tap_token, public_tag)
        if check_public_assets(release, hashes):
            raise ValueError("Both verified assets must be present before publishing.")
        if release["draft"]:
            gh(tap_token, "release", "edit", public_tag, "-R", TAP,
               "--draft=false", "--latest=false")

    if updated != formula:
        # The file SHA prevents overwriting a concurrent formula change.
        gh(tap_token, "api", "--method", "PUT", formula_endpoint, "--input", "-", payload={
            "branch": "main",
            "message": f"Update Braindex to {tag}",
            "sha": file["sha"],
            "content": base64.b64encode(updated.encode()).decode(),
        })
    print(f"Homebrew tap verified at {tag}.")


if __name__ == "__main__":
    try:
        if not os.environ.get("TAP_TOKEN"):
            raise ValueError("Set the HOMEBREW_TAP_TOKEN Actions secret. See docs/releasing.md.")
        mirror(os.environ["BRAINDEX_RELEASE_TAG"], os.environ["GITHUB_REPOSITORY"],
               os.environ["SOURCE_TOKEN"], os.environ["TAP_TOKEN"])
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        raise SystemExit(str(error))
