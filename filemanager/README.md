# Agentize File Manager

`filemanager` is the single owner of the user-document filesystem exposed by
host applications. It operates on Agentize `user_files` metadata and the shared
byte store—the knowledge tree is not part of this feature. A host mounts
`NewUserHandler` and supplies only an authenticated-user resolver.

## Layers

- `UserService`: owner-scoped operations (`List`, `Read`, `Upload`, `Write`,
  `CreateFolder`, `Move`, `Delete`). Slash-separated names form each user's
  private virtual tree; byte-store keys remain opaque.
- `UserHandler`: portable `net/http` adapter with authentication, ownership,
  multipart upload, preview/download, JSON errors, and status codes.
- Host UI: maintains presentation-only state such as open editor tabs.

## HTTP contract

- `GET /entries?path=` lists one directory.
- `POST /entries` creates a directory (`kind=directory`) or an empty/simple
  text file (`kind=file`, optional `mime_type` and `content`). `.txt`, `.md`,
  and `.csv` names get matching types when MIME is omitted.
- `DELETE /entries?id=&recursive=` deletes an owned entry.
- `POST /upload` uploads one file into the requested virtual folder.
- `GET /file?id=&mode=full|head|tail|lines&start=&end=&limit=` reads text.
- `PUT /file` replaces file content.
- `POST /move` renames or moves an entry. A destination that is an existing
  folder receives the entry (`notes` + `readme.txt` → `notes/readme.txt`).
- `GET /raw?id=&download=1` previews or downloads original bytes.

Text reads are capped at 2 MiB, scanner lines at 1 MiB, and HTTP uploads at
20 MiB. Raw downloads preserve a resolved MIME type and filename. Image and
PDF previews are inline; other types stay attachment-only. Empty or
`application/octet-stream` uploads are typed from the filename.
