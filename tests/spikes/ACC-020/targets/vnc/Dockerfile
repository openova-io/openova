# Throwaway VNC target of lab test ACC-020: Xvfb + x11vnc (password auth),
# an xterm to draw something, and xclip so the workflow can read and set the
# X clipboard that x11vnc synchronises with the VNC client (guacd).
FROM docker.io/library/alpine:3.20.3
RUN apk add --no-cache xvfb x11vnc xterm xclip ttf-dejavu
RUN printf '%s\n' \
  '#!/bin/sh' \
  'Xvfb :0 -screen 0 1024x768x24 -nolisten tcp &' \
  'sleep 2' \
  'DISPLAY=:0 xterm -fa "DejaVu Sans Mono" -fs 11 -geometry 90x30+20+20 &' \
  'exec x11vnc -display :0 -forever -shared -passwd spike-vnc -rfbport 5900 -noxdamage' \
  > /vnc-entry.sh && chmod +x /vnc-entry.sh
EXPOSE 5900
CMD ["/vnc-entry.sh"]
