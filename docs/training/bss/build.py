import os, sys
sys.path.insert(0, os.path.dirname(__file__))
from decklib import Deck
import content_a, content_b

SP = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
out = sys.argv[1] if len(sys.argv) > 1 else os.path.join(SP, "catalyst-bss-training.pptx")
d = Deck(os.path.join(SP, "shots"))
content_a.add(d)
content_b.add(d)
n = d.save(out)
print(f"{n} slides -> {out}")
