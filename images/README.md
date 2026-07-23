# CTF Agent Pi images

The images are rebuilt from slim upstream bases. They intentionally do not inherit from the legacy OpenCode images because deleting files from a derived image does not remove the old layers.

Build all profiles from the repository root:

```powershell
./images/build.ps1 -Version 0.1.0
```

The default process is Pi RPC over stdin/stdout JSONL. The control plane overrides the model and session name for each task.

Runtime policy:

- `web`, `crypto`, `forensics`, `misc`, and static `reverse`: prefer `runsc`, fall back to constrained `runc` during local development.
- `pwn` and dynamic `reverse`: prefer Kata or a dedicated VM. Local `runc` requires explicit `SYS_PTRACE` and must never use `--privileged`.
