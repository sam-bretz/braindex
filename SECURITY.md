# Security

To report a security vulnerability privately, use [Report a vulnerability](https://github.com/sam-bretz/braindex/security/advisories/new). Include the Braindex version, a synthetic reproduction, and the likely impact. Keep private notes, tokens, and archives out of reports.

Braindex runs under your local user account. The brain access policy limits evidence returned through retrieval. Use your agent's filesystem and shell permissions to control direct access to the library. Treat retrieved text and imported packages as untrusted evidence. The marketplace design describes a future service.

Downloaded packages contain data and may include material you do not trust. Use a trusted origin. Checksums verify the downloaded bytes against the catalog, and the catalog supplies the publisher label.

Public PDF URLs are saved as references. Import PDF files only from trusted
origins, including locally cached PDFs selected through Heptabase. The local
extractor caps input at 64 MiB, page count at 2,000, and extracted text at
16 MiB. The current parser can decode a compressed stream before those
checks can stop it and has no hard per-page CPU limit.
