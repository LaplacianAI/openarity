import { Template } from 'e2b'

// The room image, as an E2B template. A sandbox starts from the image as it is:
// there is no start command here, because the brain runs
// `sandbox shim -- <command>` in the sandbox itself, once it exists.
//
// `latest` is the newest build of main. Set SANDBOX_IMAGE to a sha-<commit>
// tag for a template that is one known image and not whatever main becomes.
const image =
  process.env.SANDBOX_IMAGE ?? 'ghcr.io/laplacianai/openarity-sandbox:latest'

export const template = Template().fromImage(image)
