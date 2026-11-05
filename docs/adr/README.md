# Architecture Decision Records

One ADR per decision. The file is born as `9999-<slug>.md` and is renamed to
the number of the PR that introduces it, right after the PR is opened.
Possible statuses: `proposed`, `accepted`, `superseded by NNNN`, `rejected`.

| ADR | Title | Status |
|---|---|---|
| [0026](0026-workspace-isolation.md) | Workspace isolation and where Forja runs | proposed |
| [0027](0027-pilot-repository.md) | Pilot repository and phase-0 maintainer (provisional default) | proposed — ratification pending |
| [0029](0029-workspace-agent.md) | Agent that runs in the workspace (provisional default) | proposed — ratification pending |
| [0033](0033-reservation-by-ref-with-lease.md) | Issue reservation by ref with lease | proposed |
| [0035](0035-egress-gateway.md) | Per-run egress gateway | proposed |
| [0037](0037-registry-proxy.md) | Per-run registry proxy | proposed |
| [0038](0038-model-proxy.md) | Model proxy and the agent in the workspace | proposed |
| [0042](0042-implement-delivery.md) | From the agent's commits to a pull request with forja/gates | proposed |
| [0043](0043-run-limits.md) | The run's limits, wall_clock and max_diff_lines | proposed |
| [0044](0044-proof-apparatus.md) | The proof apparatus and the human-merge label | proposed |
| [0045](0045-run-cost.md) | Compute minutes, tokens and estimated cost per run | proposed |
| [0046](0046-gate-retry.md) | A red gate goes back to the agent up to the attempt limit | proposed |
| [0047](0047-correction-round.md) | Correction round: forja.implement on Forja's own branch | proposed |
| [0048](0048-verify-not-verifiable.md) | forja.verify, the "not verifiable" verdict and forja/qa pending | proposed |
| [0049](0049-operator-view.md) | The platform owner's view of runs per repository | proposed |
| [0050](0050-verify-inversions.md) | forja.verify inversions, the QA agent and the verdict "bites / does not bite" | proposed |
| [0051](0051-forja-as-a-separate-product.md) | Forja as a separate product, with FuseOne as its reference client | proposed |
| [0052](0052-network-transport-and-caller-authentication.md) | Network transport and caller authentication | proposed |
