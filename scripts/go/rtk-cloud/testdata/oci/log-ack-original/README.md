# Original Log ACK image metadata

These are unmodified OCI metadata responses for the immutable historical Log
image selected by `logACKNativeImage`. Retrieved read-only from GHCR on
2026-10-21 with `docker buildx imagetools inspect --raw`.

Repository: `ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api`.

| File | Requested and independently verified SHA-256 |
| --- | --- |
| `index.json` | `1e9b9820f619a39e4686f1f9260f3422b7d5c239e6a4dbfd8101e488d2472244` |
| `linux-amd64.json` | `cdaea7eb2760b2ef29278f81e3fed0574ec040ef7717be70b168451b65ac6d41` |

The index binds the Linux AMD64 manifest. The second response binds its image
configuration and layer descriptors. No image layers, credentials, deployment
receipts or native environment state are included. Fixture tests hash the exact
bytes and exercise the maintained OCI reader; they make no registry calls.
This metadata proves content addressing, not a current CI qualification.
