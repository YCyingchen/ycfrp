package version

// Version is the YCFRP release version.
// Format: s<YY><MM>.<NNN>
//   YY  - two digit release year
//   MM  - two digit release month
//   NNN - incremental change counter within that month
const Version = "s2609.033"

// KernelVersion is the bundled frp kernel version.
const KernelVersion = "0.71.0"

// ProductName is the display name of the product.
const ProductName = "YCFRP"

// Description is a short product description.
const Description = "FRP 双端管理面板，内置 frps / frpc 内核"
