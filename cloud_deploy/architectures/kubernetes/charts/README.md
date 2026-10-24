# Fixed upstream deployment templates

`zammad-17.0.2.tgz` is the unmodified official Zammad chart archive, including its
bundled dependencies. Source: https://github.com/zammad/zammad-helm/releases/download/zammad-17.0.2/zammad-17.0.2.tgz

SHA-256: `22a45241b56c2fa5f2f6495d52fefc49b5731f773c22d548bea0cae4ca768850`.

This is maintained upstream template source, not generated deployment state.
The shared renderer checks the archive against `architecture.env`, renders with
`zammad-values.yaml`, and pins every target image from the shared image settings.
Archive licenses and upstream metadata are preserved inside the original package.
