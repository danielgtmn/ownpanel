# GitHub Action

If GitHub Actions already builds your site, one more step publishes it to ownpanel:

```yaml
- uses: danielgtmn/ownpanel/actions/publish@main
  with:
    panel: https://panel.example.com
    token: ${{ secrets.OWNPANEL_TOKEN }}
    site: blog
    path: dist
```

The action packs the folder `path` as a `.tar.gz` archive, uploads it to the site, and prints the new version and the site's address. When the step is green, the new version is live.

`@main` follows the newest action. To stay on one version, use a release tag (any release after this action was added) or a commit SHA instead (GitHub recommends a SHA for actions from other people).

## Before the first run

1. **Create the site** in the panel and give it its domain(s) (see [[Sites and Publishing|Sites-and-Publishing]]).
2. **Create an access token for this one site:** in the panel, open **Access tokens**, choose **New token**, then **Read and change** and **Only specific sites**, and tick just this site. Copy the token: it is shown only once.
   A token limited to one site can publish new versions and switch versions of that site, but cannot see your other sites, create or delete sites, or manage DNS. If it leaks, only that site is at risk; delete the token in the panel and create a new one. See [[Users and Access|Users-and-Access]].
3. **Store the token as a secret** in the GitHub repository: **Settings → Secrets and variables → Actions → New repository secret**, name `OWNPANEL_TOKEN`. Never write the token into the workflow file.
4. The panel must be reachable from GitHub's runners over HTTPS (its panel domain, see [[Configuration|Configuration]]).

## Inputs and outputs

| Input | Required | Meaning |
|---|---|---|
| `panel` | yes | address of the panel, for example `https://panel.example.com` (not the site's address) |
| `token` | yes | access token with **Read and change**, best limited to this site |
| `site` | yes | name of the site in the panel |
| `path` | yes | folder with the built site; its contents become the site's root |
| `timeout` | no | seconds to wait for the upload and for the site to be served, default `300` |

| Output | Meaning |
|---|---|
| `version` | id of the new version, for example `20260131T120000.000Z-ab12cd` |
| `url` | `https://` plus the site's first domain; empty if the site has no domain |

The step also writes a short summary (site, version, addresses, size) to the workflow run.

To use the outputs, give the step an `id`:

```yaml
- uses: danielgtmn/ownpanel/actions/publish@main
  id: publish
  with:
    panel: https://panel.example.com
    token: ${{ secrets.OWNPANEL_TOKEN }}
    site: blog
    path: dist
- run: echo "Version ${{ steps.publish.outputs.version }} is live at ${{ steps.publish.outputs.url }}"
```

To show the address on the run page, put it into an environment:

```yaml
jobs:
  publish:
    runs-on: ubuntu-latest
    environment:
      name: production
      url: ${{ steps.publish.outputs.url }}
```

## What it does

1. Checks the inputs: the folder must exist and contain at least one file.
2. Packs the folder's contents. Symbolic links are replaced by the files they point to (the panel refuses links in archives). It refuses archives larger than the panel accepts (512 MB) before uploading.
3. Uploads the archive with `POST /api/sites/{site}/deployments` (see [[API|API]]). The panel unpacks it into a new version and makes it live before it answers.
4. If the panel saved the version but could not update the web server yet (only possible on a site's first version), it waits until the site's address answers, up to `timeout`.
5. Prints the version and the address and sets the outputs.

On HTTPS, a brand-new domain may need a moment longer for its certificate after the first publish.

The action runs on Linux and macOS runners and needs only `bash`, `tar`, `curl` and `jq`, which GitHub's runners have. The token is masked in the log and never passed on a command line.

## When it fails

| Message | What to do |
|---|---|
| The access token was not accepted (HTTP 401) | the token is wrong, was deleted, or belongs to another panel: create a new one and update the secret |
| This access token may not publish to the site (HTTP 403) | the token is **Read only**: create one with **Read and change** |
| Site was not found (HTTP 404) | check the site name. The panel gives the same answer for a site the token may not see, so also check that the token includes this site |
| ... does not answer like an ownpanel panel | `panel` points at something else, for example the site instead of the panel |
| Could not reach the panel | wrong address, or the panel is not reachable from the internet |
| The folder does not exist / contains no files | the build step wrote somewhere else, or `path` is wrong |
| The packed site is ... but the panel accepts at most 512 MB | leave out what is not needed on the web (source maps, original images); see the limits in [[Sites and Publishing|Sites-and-Publishing]] |
| The panel refused the archive | the message says why (for example more than 1 GiB unpacked or more than 100,000 files) |

A failed run changes nothing: the version that was live stays live.

## Examples

Each example is a complete workflow file, for example `.github/workflows/publish.yml`, that publishes on every push to `main`. Replace the panel address and the site name.

### Vite (React, Vue, Svelte, ...)

```yaml
name: Publish
on:
  push:
    branches: [main]
permissions:
  contents: read
jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: lts/*
          cache: npm
      - run: npm ci
      - run: npm run build
      - uses: danielgtmn/ownpanel/actions/publish@main
        with:
          panel: https://panel.example.com
          token: ${{ secrets.OWNPANEL_TOKEN }}
          site: blog
          path: dist
```

ownpanel serves the files as they are and does not answer unknown paths with `index.html`. A single-page app with client-side routes should use hash routes or let the build write an HTML file per route.

### Astro

Astro builds into `dist` like Vite; the workflow above works unchanged. Use the static output (the default), not a server adapter.

### Hugo

```yaml
name: Publish
on:
  push:
    branches: [main]
permissions:
  contents: read
jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          submodules: recursive # themes are often submodules
      - uses: peaceiris/actions-hugo@v3
        with:
          hugo-version: latest
          extended: true
      - run: hugo --minify --baseURL https://blog.example.com/
      - uses: danielgtmn/ownpanel/actions/publish@main
        with:
          panel: https://panel.example.com
          token: ${{ secrets.OWNPANEL_TOKEN }}
          site: blog
          path: public
```

### Jekyll

```yaml
name: Publish
on:
  push:
    branches: [main]
permissions:
  contents: read
jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: ruby/setup-ruby@v1
        with:
          ruby-version: "3.3"
          bundler-cache: true
      - run: bundle exec jekyll build
        env:
          JEKYLL_ENV: production
      - uses: danielgtmn/ownpanel/actions/publish@main
        with:
          panel: https://panel.example.com
          token: ${{ secrets.OWNPANEL_TOKEN }}
          site: blog
          path: _site
```

### Plain HTML

If the repository holds the finished files, there is nothing to build. Put them in a folder (here `public`) so the workflow files and the README are not published:

```yaml
name: Publish
on:
  push:
    branches: [main]
permissions:
  contents: read
jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: danielgtmn/ownpanel/actions/publish@main
        with:
          panel: https://panel.example.com
          token: ${{ secrets.OWNPANEL_TOKEN }}
          site: blog
          path: public
```

Do not publish the repository root (`path: .`): it would put `.git` and everything else in the repository on the web.

## Without the action

The action is a small shell script, [`actions/publish/publish.sh`](https://github.com/danielgtmn/ownpanel/blob/main/actions/publish/publish.sh). It also runs in other CI systems or on your own computer:

```sh
OWNPANEL_PANEL=https://panel.example.com OWNPANEL_TOKEN=op_... \
OWNPANEL_SITE=blog OWNPANEL_PATH=dist ./publish.sh
```

Or call the API directly, see [[API|API]].
