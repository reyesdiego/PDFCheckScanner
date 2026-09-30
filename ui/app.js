"use strict";

// The page posts one file to /detect?detail=true and draws the answer. For an
// image, every box is outlined on the image itself; a PDF's pages are not
// drawn, since the browser cannot render them without a PDF library, so its
// boxes are listed by page instead.

const els = {
  drop: document.getElementById("drop"),
  file: document.getElementById("file"),
  status: document.getElementById("status"),
  result: document.getElementById("result"),
  note: document.getElementById("note"),
  figure: document.getElementById("figure"),
  canvas: document.getElementById("canvas"),
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

// What is on screen: the answer, the decoded image, and the scale from the
// server's pixel coordinates to the image the browser decoded.
let view = null;
// Each upload takes a number, so a slow answer to an earlier upload cannot
// overwrite a later one.
let latest = 0;

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

els.showEmpty.addEventListener("change", () => draw(-1));

async function detect(file) {
  const id = ++latest;
  reset();
  setStatus(`Looking for checkboxes in ${file.name}…`);

  const body = new FormData();
  body.append("image", file);

  let resp;
  try {
    resp = await fetch("/detect?detail=true", { method: "POST", body });
  } catch {
    if (id === latest) setStatus("Could not reach the service. Is it running?", true);
    return;
  }
  let data = null;
  try {
    data = await resp.json();
  } catch {
    // An error without a JSON body; the status line below still says what.
  }
  if (id !== latest) return;

  if (!resp.ok) {
    const why = data && data.error ? data.error : resp.statusText;
    setStatus(`${resp.status}: ${why}`, true);
    return;
  }
  setStatus("");
  await show(file, data);
}

function reset() {
  view = null;
  els.result.hidden = true;
  els.figure.hidden = true;
  els.note.hidden = true;
  els.rows.replaceChildren();
}

function setStatus(text, isError = false) {
  els.status.textContent = text;
  els.status.classList.toggle("error", isError);
}

async function show(file, data) {
  const boxes = data.boxes;
  const isPDF = data.image.content_type === "application/pdf";
  const checked = boxes.filter((b) => b.is_checked).length;

  els.sumFile.textContent = data.image.filename || file.name;
  els.sumBoxes.textContent = isPDF
    ? `${boxes.length} on ${data.image.pages} page${data.image.pages === 1 ? "" : "s"}`
    : String(boxes.length);
  els.sumChecked.textContent = String(checked);
  els.sumMethod.textContent =
    data.method === "acroform" ? "the PDF's form fields" : "the pixels";

  const notes = [];
  if (isPDF) {
    notes.push(
      "PDF pages are not drawn here. Positions are pixels on each page rendered at 300 DPI."
    );
    if (data.image.pages > 10) notes.push("Only the first 10 pages were scanned.");
  }
  if (boxes.length === 0) {
    notes.push(
      "No checkboxes were found. Very small boxes (under about 12px), skewed pages and inverted scans are known limitations."
    );
  }

  fillTable(boxes, isPDF, data.method === "acroform");
  els.result.hidden = false;

  if (!isPDF) {
    const mismatch = await drawImage(file, data, boxes);
    if (mismatch) notes.push(mismatch);
  }
  if (notes.length > 0) {
    els.note.textContent = notes.join(" ");
    els.note.hidden = false;
  }
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
    tr.addEventListener("mouseenter", () => draw(i));
    tr.addEventListener("mouseleave", () => draw(-1));
    return tr;
  });
  els.rows.replaceChildren(...rows);
}

// drawImage shows the upload with its boxes, and returns a warning if the
// browser decoded it at a different size than the service did.
async function drawImage(file, data, boxes) {
  const url = URL.createObjectURL(file);
  try {
    const img = new Image();
    img.src = url;
    await img.decode();

    const w = data.image.width || img.naturalWidth;
    const h = data.image.height || img.naturalHeight;
    view = {
      img,
      boxes,
      sx: img.naturalWidth / w,
      sy: img.naturalHeight / h,
    };
    els.canvas.width = img.naturalWidth;
    els.canvas.height = img.naturalHeight;
    els.figure.hidden = false;
    draw(-1);

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

function draw(active) {
  if (!view) return;
  const ctx = els.canvas.getContext("2d");
  const { img, boxes, sx, sy } = view;
  ctx.drawImage(img, 0, 0);

  const line = Math.max(2, Math.round(Math.max(img.naturalWidth, img.naturalHeight) / 500));
  boxes.forEach((b, i) => {
    if (!b.is_checked && !els.showEmpty.checked && i !== active) return;
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
  });

  for (const tr of els.rows.children) {
    tr.classList.toggle("active", Number(tr.dataset.index) === active);
  }
}

// Hovering a box on the image highlights it and its row.
els.canvas.addEventListener("mousemove", (e) => {
  if (!view) return;
  const rect = els.canvas.getBoundingClientRect();
  const px = ((e.clientX - rect.left) * els.canvas.width) / rect.width;
  const py = ((e.clientY - rect.top) * els.canvas.height) / rect.height;
  const hit = view.boxes.findIndex((b) => {
    if (!b.is_checked && !els.showEmpty.checked) return false;
    const [x1, y1, x2, y2] = b.bbox;
    return px >= x1 * view.sx && px < x2 * view.sx && py >= y1 * view.sy && py < y2 * view.sy;
  });
  draw(hit);
});
els.canvas.addEventListener("mouseleave", () => draw(-1));
