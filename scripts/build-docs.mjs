import { app, BrowserWindow } from 'electron'
import { writeFile } from 'node:fs/promises'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

/**
 * Render the setup guide to PDF.
 *
 *     npx electron scripts/build-docs.mjs
 *
 * Electron rather than a headless Chrome invocation, because printToPDF is
 * the only route here that emits a *tagged* PDF with a document outline.
 * MikkiLens is built for someone using a screen reader, and a guide about
 * MikkiLens that a screen reader reads as one undifferentiated block of text
 * would be an odd thing to ship. Chrome's `--print-to-pdf` does neither.
 *
 * The source of truth is docs/panduan-setup.html. This file only prints it.
 */

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '..')

const source = resolve(root, 'docs', 'panduan-setup.html')
const output = resolve(root, 'docs', 'MikkiLens-Panduan-Setup.pdf')

/** Page numbers come from the printer, not the CSS: Chrome has no page counter. */
const footer = `
<div style="width:100%;font:8pt 'Segoe UI',sans-serif;color:#8a837a;
            padding:0 1.6cm;display:flex;justify-content:space-between;">
  <span>MikkiLens — Panduan Pengaturan</span>
  <span class="pageNumber"></span>
</div>`

const header = '<div></div>'

app.commandLine.appendSwitch('disable-gpu')

// `.then` rather than a top-level `await`: awaiting at the top level of an ESM
// main entry stops Electron reaching 'ready' at all, and the process then hangs
// with no output to say why.
app.whenReady().then(print).catch(fail)

async function print() {
  const window = new BrowserWindow({ show: false, width: 1240, height: 1754 })
  await window.loadURL(pathToFileURL(source).href)

  const pdf = await window.webContents.printToPDF({
    pageSize: 'A4',
    printBackground: true,
    displayHeaderFooter: true,
    headerTemplate: header,
    footerTemplate: footer,
    generateTaggedPDF: true,
    generateDocumentOutline: true,
    margins: { top: 0.75, bottom: 0.75, left: 0.85, right: 0.85 },
  })

  await writeFile(output, pdf)
  process.stdout.write(`${output}\n${(pdf.length / 1024).toFixed(0)} KB\n`)
  app.exit(0)
}

function fail(error) {
  process.stderr.write(`${error?.stack ?? error}\n`)
  app.exit(1)
}
