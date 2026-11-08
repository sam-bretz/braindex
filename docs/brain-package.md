# Brain packages

A brain package is a directory containing `brain.json` and, when the curator includes searchable material, files in `sources/`. The manifest records the package ID, version, display title, optional publisher attribution, and source entries. The schema is [brain-package.v1.schema.json](../schemas/brain-package.v1.schema.json).

## References and bundles

A reference entry contains an ID and an HTTP or HTTPS URL. Braindex excludes URLs with credentials, query strings, fragments, or recognizable local addresses. It also excludes metadata such as private titles and authors from reference entries. A local-only source needs an explicit inclusion to appear in an export.

A bundle includes selected normalized passages and their locators, with optional source attribution and a redistribution designation. Bundled text must be explicitly selected, and the source must have a nonprivate redistribution designation. Review the exported directory before distributing it. The designation records the curator's choice. Each asset lives at `sources/<id>.json` and has a SHA-256 checksum in the manifest.

```json
{
  "passages": [
    {
      "text": "Ask for a concrete example.",
      "locator": "line 2"
    }
  ]
}
```

## Installation

Braindex validates the manifest, asset paths, checksums, and passage structure before adding a brain. It imports referenced files only and treats all passage text as evidence data. A references-only source appears in the brain with unavailable text. The recipient controls which topics use the installed brain.

An installation records the package version, origin, and manifest digest in the private library. Installing the same package again at the same destination is safe. A different package cannot overwrite an existing brain through installation. The package contains no active topic rules, credentials, or agent instructions.

The manifest schema documents the JSON shape. The Go validator also checks URL safety, source ID uniqueness, file names, checksums, and size limits.

## Static catalog pilot

A catalog serves a versioned JSON index and ZIP archives from one HTTPS origin. A publisher name is an attribution label supplied by the catalog operator. The pilot verifies the catalog TLS connection, archive SHA-256, and identity fields in the package manifest. Publisher verification and signing are future work.

Create a ZIP from inside a reviewed export directory so that `brain.json` sits at the root of the archive. These commands assume `./sales-package` is the export and that it includes bundled sources. For a references-only package, omit `sources` from the `zip` command.

```sh
mkdir -p catalog
(cd sales-package && zip -r ../catalog/sales-1.0.0.zip brain.json sources)
shasum -a 256 catalog/sales-1.0.0.zip
```

Save the following as `catalog/catalog.json`, replacing the checksum and matching the package ID, version, and publisher in `brain.json`.

```json
{
  "schema_version": 1,
  "packages": [
    {
      "package_id": "sales",
      "version": "1.0.0",
      "publisher": "Fixture publisher",
      "url": "sales-1.0.0.zip",
      "sha256": "<64 lowercase hexadecimal digits>"
    }
  ]
}
```

Serve that directory locally with `python3 -m http.server 8080 --bind 127.0.0.1 --directory catalog`. Use `http://127.0.0.1:8080/catalog.json` as the catalog URL. Plain HTTP is accepted only for loopback. To host the pilot, serve the same files with HTTPS and use the HTTPS index URL. Package installation requires an explicit version.
