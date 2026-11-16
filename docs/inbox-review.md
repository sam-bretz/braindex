# Review captured material

Braindex keeps captures in the local inbox until you approve them. An approved URL is stored as a reference. An approved note or supported file is imported into the chosen brain. Run `braindex index rebuild` after approval when you want the new material to appear in retrieval.

## Review a queue

Run `braindex inbox review` at a terminal. Braindex shows each unresolved item, its original content or captured file path, and any proposed digest, rationale, and destination. Notes show a short preview. Use `braindex inbox show ID --content` to inspect the full note or locate the file.

- `approve` accepts the proposed destination or resumes an interrupted approval.
- `move BRAIN` chooses a different existing brain. You can name more than one.
- `new BRAIN` creates a destination for the item.
- `defer` keeps the item for later. `dismiss` resolves it without adding a source.
- `skip` leaves the item as it is. `quit` leaves the remainder in the inbox.

Type `yes` when the terminal displays the action you chose. `braindex inbox review ID` remains available for inspecting a single item as JSON.

## Suggest a destination with a local model

Choose the inbox IDs you want to check and an embedding model already installed
in your loopback Ollama. Use descriptive brain names and curated sources so
the model has examples of what each brain covers.

```sh
braindex inbox list
braindex inbox suggest ID1 ID2 --model nomic-embed-text
braindex inbox review
```

The command prints JSON. A suggestion includes an existing brain ID, similarity
score, and reason. It abstains when the match is weak or too close to a second
candidate. Suggestions are a read-only preview. To use one, choose
`move BRAIN` in the review and confirm the placement yourself.

A run accepts up to 20 selected notes and reads up to 4 KiB from each. Results
mark notes that were truncated. URLs and file captures remain available for
manual review. Braindex compares note text with the names and up to two
curated examples per eligible brain. It excludes denied brains and sources
shared with them. Scores are cosine similarities. The command uses only the installed loopback model. Run Ollama
with [cloud features disabled](https://docs.ollama.com/faq#how-do-i-disable-ollamas-cloud-features).

## Hand selected items to an agent

List your inbox, then choose which items to prepare. The first command emits the selected item IDs, kinds, capture times, and existing brain IDs. The second includes content or the captured attachment path. Both run locally.

```sh
braindex inbox handoff ID1 ID2
umask 077
braindex inbox handoff --include-content ID1 ID2 > handoff.json
```

Inspect `handoff.json` and decide whether to share it with your agent. A file entry contains a local path. If the agent reads that path or receives the JSON, the selected material may be sent to its configured model provider.

Ask the agent to return a JSON array with one proposal per selected item. Include its `reviewed_item_id`, `digest`, and `rationale`, then either existing `brains`, a `new_brain`, or `leave_out: true`.

```json
[
  {
    "reviewed_item_id": "YOUR_ITEM_ID",
    "digest": "A concise description of the source",
    "rationale": "Why the source belongs in the suggested brain",
    "brains": ["research"]
  }
]
```

Save the proposals for review.

```sh
braindex inbox proposals ID1 ID2 < proposals.json
braindex inbox review
```

The command reports `reviewed`, `failed`, or `missing` for each selected item. Responses for unselected items and duplicate responses are reported without changing those items. Proposals leave the final decision with the user.

## Preview and apply several decisions together

Use plan mode to collect choices without changing the library. A plan stores the item IDs, actions, and the exact destinations you chose. Braindex creates it as a private local file. Type `quit` to finish early.

```sh
braindex inbox review --plan inbox-plan.json
braindex inbox batch inbox-plan.json
braindex inbox batch inbox-plan.json --apply
braindex index rebuild
```

The first `inbox batch` command previews decisions as JSON. With `--apply`, the terminal shows the ready decisions and asks for one confirmation. Results identify each applied or failed item. After an interruption, run the same plan again. Completed decisions reuse their receipts, and an interrupted approval resumes with its recorded destinations. Items whose previews fail remain available for another plan.

You can also write a plan directly. Each entry takes an `item_id` and an `action` of `approve`, `defer`, or `dismiss`. For approvals, provide `brains`, `new_brain`, or `use_proposal: true`. Plan mode records explicit destinations so a later proposal change cannot change the saved choice.
