# sherlook

**sherlook** ("share-look") is a terminal client for Microsoft 365 **shared mailboxes**.
It talks to Microsoft Graph with an app registration's **client ID / client secret**
(OAuth2 client credentials), so no user sign-in is needed.

Features:
- several shared mailboxes, each with its own credentials
- split view: message list on the left, preview on the right (list only, with a full-screen message view, below 100 columns)
- inbox listing, newest first, loaded page by page as you scroll
- message view with headers, attachment names/sizes and the body converted from HTML to text (links shown as footnotes)
- mark read/unread, move to Deleted Items, delete permanently
- export as `.eml`

Viewing a message never marks it read. In a shared mailbox, colleagues may rely on the unread state.

Not supported (yet): folders other than the Inbox, downloading attachments, bulk actions, national clouds.

## Build

Requires Go 1.27+.

```sh
make build      # → bin/sherlook
make test
make lint
```

## Azure / Microsoft 365 setup

1. **Register an app** in the Microsoft Entra admin center → *App registrations* → *New registration*
   (single tenant, no redirect URI). Note the **Application (client) ID** and **Directory (tenant) ID**.
2. **Create a client secret** under *Certificates & secrets*. Copy the secret **value** (not its ID).
3. **Grant mail access**, using one of the two options below.

### Option A: tenant-wide (simple, broad)

*API permissions* → *Add a permission* → *Microsoft Graph* → *Application permissions* →
`Mail.ReadWrite` → *Grant admin consent*.

⚠️ This lets the app read and modify **every mailbox in the tenant**. Treat the secret accordingly.

### Option B: scoped to specific mailboxes (recommended)

Use [RBAC for Applications in Exchange Online](https://learn.microsoft.com/exchange/permissions-exo/application-rbac)
and **do not** grant `Mail.ReadWrite` in Entra. Entra and Exchange grants add up, so an unscoped
Entra consent would still give tenant-wide access.

```powershell
Connect-ExchangeOnline

# Object ID of the *Enterprise application* (service principal), not of the app registration
New-ServicePrincipal -AppId <client-id> -ObjectId <enterprise-app-object-id> -DisplayName "sherlook"

# Put the shared mailboxes into a mail-enabled security group, then scope to it
$dn = (Get-DistributionGroup sherlook-mailboxes).DistinguishedName
New-ManagementScope -Name "sherlook-mailboxes" -RecipientRestrictionFilter "MemberOfGroup -eq '$dn'"
New-ManagementRoleAssignment -App <client-id> -Role "Application Mail.ReadWrite" -CustomResourceScope "sherlook-mailboxes"

# Verify
Test-ServicePrincipalAuthorization -Identity <client-id> -Resource support@contoso.com
```

Permission changes can take a while to propagate (up to about an hour). Until then, Graph answers with 403.

## Usage

```sh
sherlook                   # uses the default config file
sherlook --config path.toml
sherlook --debug           # also writes ./sherlook-debug.log
```

On startup:
- with no mailboxes configured, the *add mailbox* form opens
- with exactly one mailbox, sherlook opens it directly
- otherwise, you pick a mailbox

When you save a mailbox, sherlook runs a connection test (token + reading one inbox message) and reports the result. The mailbox is saved either way.

### Keys

| Key | Action |
| --- | --- |
| `↑`/`k`, `↓`/`j`, `pgup`/`pgdn`, `g`/`G` | move in the list / scroll the preview |
| `enter`, `tab` | focus the preview (full-screen in narrow terminals) |
| `tab`, `esc` | back from the preview to the list |
| `r` | toggle read / unread |
| `d` | move to Deleted Items (asks y/N) |
| `D` | delete permanently (asks y/N, cannot be undone) |
| `e` | export as `.eml` (asks for the path, confirms overwrites) |
| `R` | refresh the inbox |
| `m`, `esc` (in list) | switch mailbox |
| `c` | configure mailboxes (`a` add, `e` edit, `x` remove) |
| `?` | help |
| `q`, `ctrl+c` | quit |

## Configuration

The config file is TOML, stored at `<user config dir>/sherlook/config.toml`:
- Linux: `~/.config/sherlook/config.toml`
- macOS: `~/Library/Application Support/sherlook/config.toml`
- Windows: `%AppData%\sherlook\config.toml`

You can manage it from the app (`c`) or edit it by hand:

```toml
version = 1
export_dir = "~/Downloads"   # optional; default: current directory

[[mailbox]]
address = "support@contoso.com"
label = "Support DE"          # optional
tenant_id = "00000000-0000-0000-0000-000000000000"
client_id = "00000000-0000-0000-0000-000000000000"

[[mailbox]]
address = "billing@fabrikam.com"
tenant_id = "11111111-1111-1111-1111-111111111111"
client_id = "22222222-2222-2222-2222-222222222222"
```

Every mailbox has its own credentials, even when several mailboxes use the same app registration. Addresses must be unique (case-insensitive).

### Where secrets are stored

Client secrets are entered in the app and stored in the **OS keyring**: Secret Service on Linux, Keychain on macOS, Credential Manager on Windows. Each one is saved under service `sherlook`, account `<mailbox address>`.

If no keyring is available (e.g. a headless Linux box without a Secret Service daemon), the secret is written to the config file as `client_secret = "..."` instead. sherlook warns you when this happens. The file is always written with mode `0600`, and sherlook warns if it finds broader permissions.

When editing a mailbox, leave the secret field empty to keep the current secret. Removing a mailbox also deletes its keyring entry.

## Behaviour notes

- **Soft delete** moves the message to the mailbox's *Deleted Items* folder.
  **Permanent delete** uses Graph's `permanentDelete`, and users can't recover the message.
- Throttling (429) and transient errors (503/504) are retried up to 3 times, respecting `Retry-After`. An expired or rejected token is refreshed once.
- Mail is only kept in memory. Nothing is cached on disk except the `.eml` files you export.
- `--debug` logs each request's method, URL, status and duration, plus the body of error responses. It never logs tokens, secrets or mail content.
