// A screen is a function from state to a tree of plain objects. The browser turns the tree into
// elements (toDOM); a test reads the same tree without a document (textOf, findAll) and calls its
// handlers. A redraw changes only what differs from the last draw (patch): an element drawn again
// from the same kind of node stays the same element, and a value that did not change is not set
// again, so a form being filled in keeps its text, its chosen file, the selection and the focus
// while a job frame, a refetch or an answer redraws the screen around it.

export type Handler = (event: Event) => void;
export type Props = Record<string, string | number | boolean | null | undefined | Handler>;
export interface VNode {
  tag: string;
  props: Props;
  children: Node_[];
}
export type Node_ = VNode | string;
export type Child = Node_ | number | null | undefined | false | Child[];

export function h(tag: string, props: Props | null, ...children: Child[]): VNode {
  const out: Node_[] = [];
  const add = (c: Child): void => {
    if (Array.isArray(c)) c.forEach(add);
    else if (typeof c === "number") out.push(String(c));
    else if (c !== null && c !== undefined && c !== false) out.push(c);
  };
  children.forEach(add);
  return { tag, props: props ?? {}, children: out };
}

// What an element was drawn from: the key that matches it to the next draw, the props and the handlers.
interface Drawn {
  key: string;
  props: Props;
  on: Record<string, Handler>;
}
const drawn = new WeakMap<object, Drawn>();
const TEXT_NODE = 3;

// keyOf matches a node of the next draw to an element of the last one: the same tag and the same
// prop key, data-test or name. The prop key is never written to the element; a form whose key
// changes is drawn afresh, empty.
function keyOf(node: VNode): string {
  const k = node.props.key ?? node.props["data-test"] ?? node.props.name;
  return `${node.tag}|${typeof k === "string" || typeof k === "number" ? k : ""}`;
}

function setProp(el: Element, key: string, value: Props[string]): void {
  if (key === "value") (el as HTMLInputElement).value = String(value ?? "");
  else if (value === true) el.setAttribute(key, "");
  else if (value === false || value === null || value === undefined) el.removeAttribute(key);
  else el.setAttribute(key, String(value));
}

function listen(el: Element, rec: Drawn, type: string): void {
  el.addEventListener(type, (e) => rec.on[type]?.(e));
}

export function toDOM(node: Node_, doc: Document): Node {
  if (typeof node === "string") return doc.createTextNode(node);
  const el = doc.createElement(node.tag);
  const rec: Drawn = { key: keyOf(node), props: node.props, on: {} };
  drawn.set(el, rec);
  // Children first: a select takes its value only once its options exist.
  for (const child of node.children) el.appendChild(toDOM(child, doc));
  for (const [key, value] of Object.entries(node.props)) {
    if (key === "key") continue;
    if (typeof value === "function") {
      rec.on[key.slice(2)] = value;
      listen(el, rec, key.slice(2));
    } else if (key === "value" || (value !== false && value !== null && value !== undefined)) setProp(el, key, value);
  }
  return el;
}

// update makes el, drawn from a node with the same key, show node. A prop equal to the one of the
// last draw is left alone: what a person typed or chose there since stays.
function update(el: Element, node: VNode, doc: Document): void {
  const rec = drawn.get(el) as Drawn;
  patch(el, node.children, doc);
  for (const [key, value] of Object.entries(node.props)) {
    if (key === "key" || value === rec.props[key]) continue;
    if (typeof value === "function") {
      const type = key.slice(2);
      if (!rec.on[type]) listen(el, rec, type);
      rec.on[type] = value;
    } else setProp(el, key, value);
  }
  for (const key of Object.keys(rec.props)) {
    if (key in node.props || key === "key") continue;
    if (typeof rec.props[key] === "function") delete rec.on[key.slice(2)];
    else if (key !== "value") el.removeAttribute(key);
  }
  rec.props = node.props;
}

// patch makes the children of parent show kids. A child keeps its element when the last draw has
// one with the same key; the others are created, and elements no node took are removed first, so
// an element that stays is not moved (a browser takes the focus from a moved element).
function patch(parent: Node, kids: Node_[], doc: Document): void {
  const old = Array.from(parent.childNodes);
  const free = new Map<string, Node[]>();
  for (const o of old) {
    const k = o.nodeType === TEXT_NODE ? "#text" : (drawn.get(o)?.key ?? "");
    free.set(k, [...(free.get(k) ?? []), o]);
  }
  const next = kids.map((kid): Node => {
    const match = free.get(typeof kid === "string" ? "#text" : keyOf(kid))?.shift();
    if (match === undefined) return toDOM(kid, doc);
    if (typeof kid !== "string") update(match as Element, kid, doc);
    else if (match.nodeValue !== kid) match.nodeValue = kid;
    return match;
  });
  for (const o of old) if (!next.includes(o)) parent.removeChild(o);
  next.forEach((n, i) => {
    if (parent.childNodes[i] !== n) parent.insertBefore(n, parent.childNodes[i] ?? null);
  });
}

// mount draws view into root and returns the function that draws it again.
export function mount(root: HTMLElement, view: () => VNode): () => void {
  const draw = (): void => patch(root, [view()], root.ownerDocument);
  draw();
  return draw;
}

export function textOf(node: Node_): string {
  return typeof node === "string" ? node : node.children.map(textOf).join("");
}

export function findAll(node: Node_, match: (n: VNode) => boolean): VNode[] {
  if (typeof node === "string") return [];
  const own = match(node) ? [node] : [];
  return own.concat(...node.children.map((c) => findAll(c, match)));
}

// byTest finds the one element whose data-test attribute is name.
export function byTest(node: Node_, name: string): VNode {
  const found = findAll(node, (n) => n.props["data-test"] === name);
  if (found.length !== 1) throw new Error(`data-test=${name}: ${found.length} elements`);
  return found[0];
}
