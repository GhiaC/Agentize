# Agentize File Manager

`filemanager` is the single owner of filesystem operations used by host
applications. A host supplies one knowledge root, mounts `NewHandler`, and
builds its UI against the HTTP contract. Hosts must not resolve paths or touch
files themselves.

## Layers

- `Service`: sandbox and operations (`List`, `Read`, `Write`, `Mkdir`, `Move`,
  `Delete`). Paths are always root-relative. Traversal, root mutation, and
  symlink escape are rejected.
- `Handler`: portable `net/http` adapter with JSON errors and status codes.
- Host UI: maintains presentation-only state such as open editor tabs.

## HTTP contract

- `GET /entries?path=` lists one directory.
- `POST /entries` creates a file or directory.
- `DELETE /entries?path=&recursive=` deletes an entry.
- `GET /file?path=&mode=full|head|tail|lines&start=&end=&limit=` reads text.
- `PUT /file` replaces file content.
- `POST /move` renames or moves an entry.

The default read ceiling is 2 MiB and the default scanner line ceiling is
1 MiB. Both are configurable through `Config`.
