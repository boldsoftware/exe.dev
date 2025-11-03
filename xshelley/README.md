# xshelley

This is the "extract shelley" package, which downloads the shelley binary from
the published `ghcr.io/boldsoftware/exeuntu` multi-arch Docker image.

The deployment process of exeuntu is continuous whereas the deployment process of
exed is explicit with make deploy-exed. Having this library is a choice to
allow upgrading shelley whenever based on the exeuntu package rathre than
embedding shelley into the exed binary.

Downloading the entire image and extracting a file typically involves
starting a container with the image. Instead, we download the metadata,
find the step that copied the shelley binary into the image, download just
that layer (which is reasonably sized), and extract Shelley.
