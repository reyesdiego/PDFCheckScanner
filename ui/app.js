"use strict";

// The page posts one file to /detect?detail=true and draws the answer. An
// image is drawn from the upload itself; a PDF's pages are drawn by the
// service, which /preview renders, since a browser cannot draw a PDF onto a
// canvas without a PDF library. Either way each picture is a "view", and the
// boxes on it are outlined where the answer says they are.

const els = {
  drop: document.getElementById("drop"),
  file: document.getElementById("file"),
  status: document.getElementById("status"),
  result: document.getElementById("result"),
  note: document.getElementById("note"),
  drawing: document.getElementById("drawing"),
  views: document.getElementById("views"),
  showEmpty: document.getElementById("show-empty"),
  table: document.getElementById("boxes"),
  rows: document.querySelector("#boxes tbody"),
  sumFile: document.getElementById("sum-file"),
  sumBoxes: document.getElementById("sum-boxes"),
  sumChecked: document.getElementById("sum-checked"),
  sumMethod: document.getElementById("sum-method"),
};

const colors = {
  checked: "#1a7f37",
  empty: "#cf222e",
  highlight: "#f5b400",
};

// What is on screen. Each view is one picture: its canvas, the decoded image,
// the boxes on it with their index in the answer, and the scale from the
// answer's pixel coordinates to the picture.
let views = [];
// Each upload takes a number, so a slow answer to an earlier upload cannot
// overwrite a later one.
let latest = 0;
// The box under the pointer, by index in the answer, or -1.
let active = -1;

els.file.addEventListener("change", () => {
  if (els.file.files.length > 0) detect(els.file.files[0]);
});

for (const type of ["dragenter", "dragover"]) {
  els.drop.addEventListener(type, (e) => {
    e.preventDefault();
    els.drop.classList.add("dragging");
  });
}
for (const type of ["dragleave", "drop"]) {
  els.drop.addEventListener(type, () => els.drop.classList.remove("dragging"));
}
els.drop.addEventListener("drop", (e) => {
  e.preventDefault();
  const file = e.dataTransfer.files[0];
  if (file) detect(file);
});

els.showEmpty.addEventListener("change", () => highlight(-1));

// post sends the file to path and returns the parsed answer, or throws an
// Error whose message is fit to show.
async function post(path, file) {
  const body = new FormData();
  body.append("image", file);

  let resp;
  try {
    resp = await fetch(path, { method: "POST", body });
  } catch {
    throw new Error("Could not reach the service. Is it running?");
  }
  let data = null;
  try {
    data = await resp.json();
  } catch {
    // An error without a JSON body; the status says what.
  }
  if (!resp.ok) {
    const why = data && data.error ? data.error : resp.statusText;
    throw new Error(`${resp.status}: ${why}`);
  }
  return data;
}

async function detect(file) {
  const id = ++latest;
  reset();
  setStatus(`Looking for checkboxes in ${file.name}…`);

  let data;
  try {
    data = await post("/detect?detail=true", file);
  } catch (err) {
    if (id === latest) setStatus(err.message, true);
    return;
  }
  if (id !== latest) return;
  setStatus("");

  const isPDF = data.image.content_type === "application/pdf";
  const notes = summarise(file, data, isPDF);
  fillTable(data.boxes, isPDF, data.method === "acroform");
  els.result.hidden = false;

  if (isPDF) {
    setStatus("Drawing the pages…");
    try {
      const preview = await post("/preview", file);
      if (id !== latest) return;
      await showPages(preview.pages, data.boxes);
    } catch (err) {
      if (id !== latest) return;
      notes.push(`The pages could not be drawn (${err.message}), so only the table is shown.`);
    }
    setStatus("");
  } else {
    const warning = await showImage(file, data);
    if (id !== latest) return;
    if (warning) notes.push(warning);
  }
  if (notes.length > 0) {
    els.note.textContent = notes.join(" ");
    els.note.hidden = false;
  }
}

function reset() {
  views = [];
  active = -1;
  els.result.hidden = true;
  els.drawing.hidden = true;
  els.note.hidden = true;
  els.views.replaceChildren();
  els.rows.replaceChildren();
}

function setStatus(text, isError = false) {
  els.status.textContent = text;
  els.status.classList.toggle("error", isError);
}

// summarise fills in the headline numbers and returns the notes that apply.
function summarise(file, data, isPDF) {
  const boxes = data.boxes;
  const pages = data.image.pages;
  els.sumFile.textContent = data.image.filename || file.name;
  els.sumBoxes.textContent = isPDF
    ? `${boxes.length} on ${pages} page${pages === 1 ? "" : "s"}`
    : String(boxes.length);
  els.sumChecked.textContent = String(boxes.filter((b) => b.is_checked).length);
  els.sumMethod.textContent =
    data.method === "acroform" ? "the PDF's form fields" : "the pixels";

  const notes = [];
  if (isPDF && pages > 10) {
    notes.push("Only the first 10 pages were scanned and drawn.");
  }
  if (boxes.length === 0) {
    notes.push(
      "No checkboxes were found. Very small boxes (under about 12px), skewed pages and inverted scans are known limitations."
    );
  }
  return notes;
}

function fillTable(boxes, isPDF, fromFields) {
  els.table.classList.toggle("no-page", !isPDF);
  els.table.classList.toggle("no-name", !fromFields);

  const rows = boxes.map((b, i) => {
    const [x1, y1, x2, y2] = b.bbox;
    const tr = document.createElement("tr");
    tr.dataset.index = String(i);
    const cells = [
      [String(i + 1)],
      [b.page ? String(b.page) : "", "col-page"],
      [b.is_checked ? "Marked" : "Empty", b.is_checked ? "state-checked" : "state-empty"],
      [`${x1}, ${y1} – ${x2}, ${y2}`],
      [`${x2 - x1} × ${y2 - y1}`],
      [b.confidence === undefined ? "" : b.confidence.toFixed(2)],
      [b.name || "", "col-name"],
    ];
    for (const [text, cls] of cells) {
      const td = document.createElement("td");
      td.textContent = text;
      if (cls) td.className = cls;
      tr.append(td);
    }
    tr.addEventListener("mouseenter", () => highlight(i));
    tr.addEventListener("mouseleave", () => highlight(-1));
    return tr;
  });
  els.rows.replaceChildren(...rows);
}

// showImage draws an image upload with its boxes, and returns a warning if the
// browser decoded it at a different size than the service did.
async function showImage(file, data) {
  const url = URL.createObjectURL(file);
  try {
    const img = await load(url);
    const w = data.image.width || img.naturalWidth;
    const h = data.image.height || img.naturalHeight;
    const boxes = data.boxes.map((b, i) => ({ b, i }));
    addView(img, boxes, img.naturalWidth / w, img.naturalHeight / h, "");

    // A photo with an EXIF rotation tag is shown turned by the browser but
    // read as stored by the service, and the boxes will not line up.
    if (img.naturalWidth === h && img.naturalHeight === w && w !== h) {
      return "This image carries a rotation tag that the detector ignores, so the outlines may not line up. Re-save it upright to fix that.";
    }
    return "";
  } catch {
    return "The browser could not display this image, so only the table is shown.";
  } finally {
    URL.revokeObjectURL(url);
  }
}

// showPages draws each previewed PDF page with the boxes found on it.
async function showPages(pages, boxes) {
  for (const p of pages) {
    const img = await load(p.image);
    const onPage = boxes.map((b, i) => ({ b, i })).filter(({ b }) => b.page === p.page);
    const count = onPage.length === 1 ? "1 checkbox" : `${onPage.length} checkboxes`;
    addView(img, onPage, p.scale, p.scale, `Page ${p.page} · ${count}`);
  }
}

function load(src) {
  const img = new Image();
  img.src = src;
  return img.decode().then(() => img);
}

function addView(img, boxes, sx, sy, caption) {
  const figure = document.createElement("figure");
  if (caption) {
    const fc = document.createElement("figcaption");
    fc.textContent = caption;
    figure.append(fc);
  }
  const canvas = document.createElement("canvas");
  canvas.width = img.naturalWidth;
  canvas.height = img.naturalHeight;
  canvas.setAttribute(
    "aria-label",
    caption ? `${caption}, with each checkbox outlined` : "The upload, with each checkbox outlined"
  );
  figure.append(canvas);
  els.views.append(figure);
  els.drawing.hidden = false;

  const view = { canvas, img, boxes, sx, sy };
  views.push(view);
  draw(view);

  // Hovering a box on the picture highlights it and its row.
  canvas.addEventListener("mousemove", (e) => {
    const rect = canvas.getBoundingClientRect();
    const px = ((e.clientX - rect.left) * canvas.width) / rect.width;
    const py = ((e.clientY - rect.top) * canvas.height) / rect.height;
    const hit = view.boxes.find(({ b }) => {
      if (!b.is_checked && !els.showEmpty.checked) return false;
      const [x1, y1, x2, y2] = b.bbox;
      return px >= x1 * sx && px < x2 * sx && py >= y1 * sy && py < y2 * sy;
    });
    highlight(hit ? hit.i : -1);
  });
  canvas.addEventListener("mouseleave", () => highlight(-1));
}

// highlight makes box i, by index in the answer, the active one everywhere.
function highlight(i) {
  active = i;
  for (const view of views) draw(view);
  for (const tr of els.rows.children) {
    tr.classList.toggle("active", Number(tr.dataset.index) === active);
  }
}

function draw(view) {
  const { canvas, img, boxes, sx, sy } = view;
  const ctx = canvas.getContext("2d");
  ctx.drawImage(img, 0, 0);

  const line = Math.max(2, Math.round(Math.max(img.naturalWidth, img.naturalHeight) / 500));
  for (const { b, i } of boxes) {
    if (!b.is_checked && !els.showEmpty.checked && i !== active) continue;
    const [x1, y1, x2, y2] = b.bbox;
    const x = x1 * sx, y = y1 * sy, w = (x2 - x1) * sx, h = (y2 - y1) * sy;
    const color = b.is_checked ? colors.checked : colors.empty;
    if (b.is_checked) {
      ctx.fillStyle = color + "40";
      ctx.fillRect(x, y, w, h);
    }
    ctx.lineWidth = i === active ? line * 2 : line;
    ctx.strokeStyle = i === active ? colors.highlight : color;
    ctx.strokeRect(x - line / 2, y - line / 2, w + line, h + line);
  }
}
