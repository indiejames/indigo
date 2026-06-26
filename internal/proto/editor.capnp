using Go = import "/go.capnp";
@0xd75b3b54eb3ed6a2;
$Go.package("proto");
$Go.import("github.com/indiejames/indigo/internal/proto");

# EditorService is the capability exposed by the server over a Unix socket.
interface EditorService {
  connect         @0 ()                                                        -> (clientId :UInt64);
  disconnect      @1 (clientId :UInt64)                                        -> ();
  openFile        @2 (clientId :UInt64, path :Text)                            -> (bufferId :UInt32, content :Text, version :UInt64, fromRecovery :Bool);
  getUpdates      @3 (clientId :UInt64, bufferId :UInt32, sinceVersion :UInt64) -> (ops :List(EditOp), version :UInt64);
  applyOp         @4 (clientId :UInt64, bufferId :UInt32, op :EditOp)          -> (version :UInt64);
  save            @5 (clientId :UInt64, bufferId :UInt32)                      -> ();
  closeBuffer     @6 (clientId :UInt64, bufferId :UInt32)                      -> ();
  bufferClientCount @7 (bufferId :UInt32)                                      -> (count :UInt32);
  discardRecovery @8 (clientId :UInt64, bufferId :UInt32)                      -> (content :Text);
  getDiagnostics  @9  (bufId :UInt32)                                          -> (items :List(LspDiagnostic));
  hover           @10 (bufId :UInt32, line :UInt32, col :UInt32)               -> (result :HoverResult);
  signatureHelp   @11 (bufId :UInt32, line :UInt32, col :UInt32)               -> (result :SignatureHelp);
  complete        @12 (bufId :UInt32, line :UInt32, col :UInt32)               -> (items :List(CompletionItem));
}

struct LspDiagnostic {
  line     @0 :UInt32;
  col      @1 :UInt32;
  endLine  @2 :UInt32;
  endCol   @3 :UInt32;
  severity @4 :UInt8;
  message  @5 :Text;
  source   @6 :Text;
}

struct HoverResult {
  contents @0 :Text;
  found    @1 :Bool;
}

struct SignatureParam {
  label @0 :Text;
}

struct SignatureInfo {
  label           @0 :Text;
  documentation   @1 :Text;
  parameters      @2 :List(SignatureParam);
  activeParameter @3 :UInt32;
}

struct SignatureHelp {
  signatures      @0 :List(SignatureInfo);
  activeSignature @1 :UInt32;
  activeParameter @2 :UInt32;
  found           @3 :Bool;
}

struct CompletionItem {
  label      @0 :Text;
  kind       @1 :UInt8;
  detail     @2 :Text;
  insertText @3 :Text;
}

struct EditOp {
  clientId  @0 :UInt64;
  version   @1 :UInt64;
  type      @2 :OpType;

  # Insert fields
  insertLine @3 :UInt32;
  insertCol  @4 :UInt32;
  insertText @5 :Text;

  # Delete fields
  fromLine @6 :UInt32;
  fromCol  @7 :UInt32;
  toLine   @8 :UInt32;
  toCol    @9 :UInt32;

  enum OpType {
    noop   @0;
    insert @1;
    delete @2;
  }
}
