import * as React from 'react';
import { useEffect, useRef, useState } from 'react';
import CodeMirror, { ReactCodeMirrorRef } from '@uiw/react-codemirror';
import { sql } from '@codemirror/lang-sql';
import { Decoration, EditorView, keymap } from '@codemirror/view';
import { Extension, Prec } from '@codemirror/state';
import { autocompletion } from '@codemirror/autocomplete';
import { ThemeName } from '@clickhouse/click-ui';
import { Panel, PanelGroup, PanelResizeHandle } from 'react-resizable-panels';
import { editorChrome, editorTheme } from '../CodeMirrorTheme';
import clickhouseSqlHighlight from '../sql/highlight';
import HexViewer, { OutputRange } from './HexViewer';

// CodeMirror's default keymap binds Mod-Enter (Cmd-Enter on macOS, Ctrl-Enter
// elsewhere) to "insert blank line". Consume it here so the shortcut only
// runs the query: the event still bubbles up to the window-level listener in
// App, which triggers the run.
const swallowModEnter = Prec.highest(
  keymap.of([{ key: 'Mod-Enter', run: () => true }]),
);

// Tracks whether the viewport is narrow enough to stack the panels vertically.
function useIsMobile(): boolean {
  const [isMobile, setIsMobile] = useState(() => window.matchMedia('(max-width: 600px)').matches);

  useEffect(() => {
    const mediaQuery = window.matchMedia('(max-width: 600px)');
    const onChange = (event: MediaQueryListEvent) => setIsMobile(event.matches);
    mediaQuery.addEventListener('change', onChange);
    return () => mediaQuery.removeEventListener('change', onChange);
  }, []);

  return isMobile;
}

interface EditorPanelProps {
  initialInput: string;
  output: string;
  rawOutput: Uint8Array | undefined;
  // Shown above the output when it is not the full result (cut by the server, or not stored).
  outputNotice: string;
  timeElapsed: string | undefined;
  requestIsRunning: boolean;
  followOutput: boolean;
  showHexViewer: boolean;
  theme: ThemeName;
  onInputChange: (value: string) => void;
}

function EditorPanel({
  initialInput,
  output,
  rawOutput,
  outputNotice,
  timeElapsed,
  requestIsRunning,
  followOutput,
  showHexViewer,
  theme,
  onInputChange,
}: EditorPanelProps) {
  const isMobile = useIsMobile();
  const [highlightedOutputRanges, setHighlightedOutputRanges] = useState<OutputRange[]>([]);

  const panelDirection = isMobile ? 'vertical' : 'horizontal';
  const codeMirrorTheme = editorTheme(theme);

  // While streaming build logs, keep the output scrolled to the latest line.
  const outputRef = useRef<ReactCodeMirrorRef>(null);
  useEffect(() => {
    if (!followOutput) {
      return;
    }
    const { view } = outputRef.current ?? {};
    if (view) {
      view.scrollDOM.scrollTop = view.scrollDOM.scrollHeight;
    }
  }, [output, followOutput]);

  // Each range marks the full source character for a hovered or pinned UTF-8
  // sequence. Sort the decorations because CodeMirror requires document order.
  const outputHighlight = React.useMemo<Extension>(() => {
    if (highlightedOutputRanges.length === 0) {
      return [];
    }

    const sortedRanges = highlightedOutputRanges
      .filter((range, index, ranges) => ranges.findIndex(
        (candidate) => candidate.from === range.from && candidate.to === range.to,
      ) === index)
      .sort((left, right) => left.from - right.from || left.to - right.to);
    const mergedRanges: OutputRange[] = [];
    sortedRanges.forEach((range) => {
      const previous = mergedRanges[mergedRanges.length - 1];
      if (previous && range.from <= previous.to) {
        previous.to = Math.max(previous.to, range.to);
      } else {
        mergedRanges.push({ ...range });
      }
    });

    return EditorView.decorations.of(Decoration.set(mergedRanges.map((range) => (
      Decoration.mark({ class: 'output-hex-highlight' }).range(range.from, range.to)
    ))));
  }, [highlightedOutputRanges]);

  useEffect(() => {
    setHighlightedOutputRanges([]);
  }, [output, showHexViewer]);

  const panelStyle: React.CSSProperties = {
    overflow: 'hidden',
    display: 'flex',
    flexDirection: 'column',
    height: '100%',
    minHeight: 0,
    width: '100%',
    maxWidth: '100%',
  };

  const codeMirrorStyle: React.CSSProperties = {
    height: '100%',
    width: '100%',
    flexGrow: 1,
    overflow: 'auto',
    display: 'flex',
    flexDirection: 'column',
  };

  const outputEditor = (
    <CodeMirror
      ref={outputRef}
      theme={codeMirrorTheme}
      value={output}
      basicSetup={{
        lineNumbers: false,
        foldGutter: false,
        dropCursor: true,
        indentOnInput: false,
        highlightActiveLine: false,
      }}
      style={codeMirrorStyle}
      className="cm-editor-container"
      extensions={[editorChrome, EditorView.lineWrapping, outputHighlight]}
      readOnly
    />
  );

  const outputPaneStyle: React.CSSProperties = { ...panelStyle, position: 'relative' };

  return (
    <div className="editor-area">
      <PanelGroup
        direction={panelDirection}
        style={{
          height: '100%',
          width: '100%',
          minHeight: 0,
          maxWidth: '100%',
          flex: 1,
        }}
      >
        <Panel defaultSize={isMobile ? 40 : 50} minSize={20} style={panelStyle}>
          <CodeMirror
            autoFocus
            theme={codeMirrorTheme}
            value={initialInput}
            placeholder="Enter SQL queries..."
            style={codeMirrorStyle}
            className="cm-editor-container"
            editable={!requestIsRunning}
            extensions={[
              swallowModEnter,
              editorChrome,
              EditorView.lineWrapping,
              autocompletion({
                icons: false,
              }),
              // `sql()` stays for keyword autocompletion only: the syntax colors come
              // from the ClickHouse WASM lexer, like in the ClickHouse Web UI.
              sql({
                upperCaseKeywords: true,
              }),
              clickhouseSqlHighlight(),
            ]}
            basicSetup={{
              lineNumbers: true,
              foldGutter: false,
              indentOnInput: false,
              autocompletion: true,
              highlightActiveLine: false,
            }}
            onChange={onInputChange}
          />
        </Panel>
        <PanelResizeHandle className={isMobile ? 'resize-handle-vertical' : 'resize-handle-horizontal'} />
        <Panel defaultSize={isMobile ? 60 : 50} minSize={20} style={panelStyle}>
          {showHexViewer ? (
            <PanelGroup direction="vertical" style={{ height: '100%', minHeight: 0 }}>
              <Panel defaultSize={65} minSize={20} style={outputPaneStyle}>
                {outputNotice && <div className="output-notice">{outputNotice}</div>}
                {outputEditor}
                {timeElapsed && <div className="time-elapsed">{timeElapsed}</div>}
              </Panel>
              <PanelResizeHandle className="resize-handle-vertical" />
              <Panel defaultSize={35} minSize={15} style={panelStyle}>
                <HexViewer
                  output={output}
                  rawOutput={rawOutput}
                  onHighlightRanges={setHighlightedOutputRanges}
                />
              </Panel>
            </PanelGroup>
          ) : (
            <div style={outputPaneStyle}>
              {outputNotice && <div className="output-notice">{outputNotice}</div>}
              {outputEditor}
              {timeElapsed && <div className="time-elapsed">{timeElapsed}</div>}
            </div>
          )}
        </Panel>
      </PanelGroup>
    </div>
  );
}

export default EditorPanel;
