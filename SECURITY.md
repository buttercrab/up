# Security policy

`up` executes installed package managers with your user permissions. Those tools
can run installers and request sudo themselves. `up` does not provide a sandbox
for package-manager code or a rollback guarantee.

During pre-1.0 development, security fixes target the default branch and the
latest published prerelease. Older development snapshots may require upgrading;
backports are not promised.

## Reporting a vulnerability

Use the repository's **Security → Report a vulnerability** button when private
vulnerability reporting is available:

<https://github.com/buttercrab/up/security>

If private reporting is unavailable, open a minimal issue asking the maintainer
for a private reporting channel. Do not include exploit details, credentials,
passwords, tokens, or unredacted logs in a public issue.

Provide the affected version, operating system, reproduction using harmless
fixtures where possible, and expected versus observed behavior through the
private channel. Do not test an exploit against another person's machine.

## Logs and terminal input

Full logs live under the OS user cache in `up/runs`, with private directory and
file permissions. Package-manager output may contain sensitive paths, URLs, or
tokens. Inspect and redact it before sharing.

During terminal handoff, keystrokes go to the running command. Password echo is
controlled by that command's terminal settings. `up` does not store input, but
cannot prevent a command from echoing or printing sensitive values itself.

Process-group cancellation covers descendants that stay in the command's group.
Commands that deliberately create another session or process group are outside
that boundary. Resource locks apply within one run, not across separate `up`
processes.
