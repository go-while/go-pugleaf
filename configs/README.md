# Configuration Files | BRAINSTORMING !!!

This directory contains early configuration drafts for go-pugleaf. **None of these files is read by any
binary**: no Go code opens `configs/` or loads a JSON/YAML config file.

## Files

- `config.json` - draft of a JSON layout (`max_article_size`, `providers`, `server`, `database`, `web`), roughly
  following the `MainConfig` struct in `internal/config/config.go`; its `server` keys and `database` paths
  (`data/main.db`, `data/groups`) do not match the current code
- `config.sample.json` - the same draft with a second, disabled example provider
- `sample.yaml` - older YAML draft (`database`, `nntp`, `web`, `import`); no YAML config loader exists

## Usage

The binaries are configured by command-line flags (see `README.md` and `-h` of each tool), built-in defaults
(`config.NewDefaultConfig()` in `internal/config/config.go`) and settings stored in the main database
(`<data>/cfg/pugleaf.sq3`, `-data` defaults to `./data`; group DBs under `<data>/db`). NNTP providers live in the main database: the
defaults from `config.DefaultProviders` are created disabled and are edited in the web admin
(Admin → Server). Do not copy these files expecting them to be picked up.

## Production Deployment

- Provider credentials and other settings are stored in the main database under the `-data` directory
  (default `./data/cfg/pugleaf.sq3`); protect that directory with proper file permissions
- Store the data directory outside the application directory if you can (pass `-data <dir>`)
- `config.json` and `config.sample.json` contain an example provider login; do not put real credentials here
