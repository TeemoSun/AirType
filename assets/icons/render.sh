#!/bin/bash
# 从三色主 SVG 渲染多尺寸 PNG（透明背景, 显式像素尺寸, DPR=1）
set -e
cd "D:/Code/AirType/assets/icons"
EDGE="/c/Program Files (x86)/Microsoft/Edge/Application/msedge.exe"
[ -f "$EDGE" ] || EDGE="/c/Program Files/Microsoft/Edge/Application/msedge.exe"

render_one() {
  local color=$1 size=$2
  cat > "render_tmp.html" <<EOF
<html><head><style>html,body{margin:0;padding:0;overflow:hidden;background:transparent}img{display:block;width:${size}px;height:${size}px}</style></head>
<body><img src="file:///D:/Code/AirType/assets/icons/airtype-${color}.svg"></body></html>
EOF
  "$EDGE" --headless=new --disable-gpu --force-device-scale-factor=1 \
    --default-background-color=00000000 --hide-scrollbars \
    --window-size=$size,$size --screenshot="D:/Code/AirType/assets/icons/airtype-${color}_${size}.png" \
    "file:///D:/Code/AirType/assets/icons/render_tmp.html" 2>/dev/null
}

for color in green yellow red; do
  for size in 512 256 128 64 32 16; do
    render_one "$color" "$size"
    echo "airtype-${color}_${size}.png"
  done
done
rm -f render_tmp.html
echo RENDER_ALL_DONE
