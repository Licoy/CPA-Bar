#!/bin/sh
# Renders the app icon and the tray icons from the logo SVGs in resources/.
# Needs ImageMagick 7 (magick). Run from anywhere; the outputs are committed.
#
#   resources/icon.png                  the app icon (mygo.json), 1024 px
#   resources/cpa-bar-elephant-color.png transparent color logo, 1254 px
#   resources/cpa-bar-elephant-05-large-v1.png color logo on a pale blue background
#   internal/view/assets/logo.svg       the logo in the app (About)
#   internal/view/assets/app-512.png    the window and Dock icon at run time
#   internal/view/assets/tray-*.png     the tray icons: color and mono, at
#                                       36 px (macOS, 18 pt) and 32 px
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
res="$root/resources"
out="$root/internal/view/assets"
mkdir -p "$out"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

render() { # svg size file
	magick -background none -density 3000 "$1" -resize "$2x$2" "$3"
}

# Tray icons: the logo with a little room around it.
for kind in color black; do
	render "$res/cpa-bar-elephant-$kind.svg" 1024 "$tmp/$kind.png"
	name=$kind
	[ "$kind" = black ] && name=mono
	magick "$tmp/$kind.png" -filter Lanczos -resize 32x32 -gravity center -background none -extent 36x36 "PNG32:$out/tray-$name-36.png"
	magick "$tmp/$kind.png" -filter Lanczos -resize 30x30 -gravity center -background none -extent 32x32 "PNG32:$out/tray-$name-32.png"
done

# Standalone color assets, rendered directly from the same SVG palette.
magick -background none -density 5016 "$res/cpa-bar-elephant-color.svg" "PNG32:$res/cpa-bar-elephant-color.png"
magick "$res/cpa-bar-elephant-color.png" -background '#E8F3FF' -alpha remove -alpha off "$res/cpa-bar-elephant-05-large-v1.png"

# App icon: the color logo on a pale blue rounded square, on Apple's icon grid
# (an 824 px square with 185 px corners on a 1024 px canvas).
render "$res/cpa-bar-elephant-color.svg" 600 "$tmp/logo.png"
magick -size 1024x1024 xc:none \
	\( -size 824x824 xc:none -fill '#E8F3FF' -draw 'roundrectangle 0,0 823,823 185,185' \) \
	-gravity center -composite \
	"$tmp/logo.png" -gravity center -geometry +0+6 -composite \
	"PNG32:$res/icon.png"
magick "$res/icon.png" -filter Lanczos -resize 512x512 "PNG32:$out/app-512.png"

cp "$res/cpa-bar-elephant-color.svg" "$out/logo.svg"
echo "icons written to $res/icon.png and $out"
