# Founder-led sales walkthrough

This fixture contains synthetic notes for testing citation and routing.

1. Create the `founder-sales` brain and import `discovery.md`.
2. Add a `founder-led-sales` topic rule that matches `founder-led sales`, then rebuild the index.
3. Ask `How should I run founder-led sales discovery?`. Expect `matched`, the `founder-sales` brain, and cited passages whose line locators resolve in `discovery.md`.
4. Ask `What is the weather?`. Expect `unmatched`.
5. Run `braindex context xylophonestorm --topic founder-led-sales --json`. Expect the selected brain to report `no_relevant_passages`.

Follow the [agent instructions](../../docs/agent-integration.md) to exercise the same flow through a coding agent.
