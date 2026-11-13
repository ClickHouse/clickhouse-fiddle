export type GetTagsResponse = {
  tags: string[];
};

export type GetBuildTypesResponse = {
  buildTypes: string[];
};

// ImageBuildState mirrors the backend qrunner.ImageState values.
export type ImageBuildState = 'building' | 'ready' | 'failed';

export type ImageStatusResponse = {
  state: ImageBuildState;
  detail?: string;
  logs?: string;
  error?: string;
};

export type GetQueryRunResponse = {
  version: string;
  buildType: string;
  input: string;
  output: string;
  outputBytes: Uint8Array;
  // The server cut the output at its size cap during the run.
  outputTruncated: boolean;
  // The output was not stored: it did not fit the storage item size limit.
  outputDropped: boolean;
  queryRunId: string;
};

export type RunQueryResponse = {
  queryRunId: string;
  output: string;
  outputBytes: Uint8Array;
  outputTruncated: boolean;
  outputDropped: boolean;
  timeElapsed: string;
};

export const RELEASE_BUILD_TYPE = 'release';

function textToBytes(text: string): Uint8Array {
  return new TextEncoder().encode(text);
}

// Older API deployments do not recognize include_raw_output. They return the
// legacy text field, which is still a useful UTF-8 fallback for the hex view.
function decodeOutputBytes(output: string, outputBase64: unknown): Uint8Array {
  if (typeof outputBase64 !== 'string') {
    return textToBytes(output);
  }

  try {
    const binary = window.atob(outputBase64);
    const bytes = new Uint8Array(binary.length);
    for (let index = 0; index < binary.length; index += 1) {
      bytes[index] = binary.charCodeAt(index);
    }
    return bytes;
  } catch {
    return textToBytes(output);
  }
}

export class Client {
  apiBaseUrl: string;

  constructor(apiUrl: string) {
    this.apiBaseUrl = apiUrl;
  }

  public getTags(): Promise<GetTagsResponse> {
    return fetch(`${this.apiBaseUrl}tags`)
      .then((response) => response.json())
      .then((response) => {
        if (response.result) {
          return {
            tags: response.result.tags,
          };
        }
        throw Error(response.error.message);
      })
      .catch((error) => {
        throw error;
      });
  }

  public getBuildTypes(): Promise<GetBuildTypesResponse> {
    return fetch(`${this.apiBaseUrl}build-types`)
      .then((response) => response.json())
      .then((response) => {
        if (response.result) {
          return {
            buildTypes: response.result.build_types,
          };
        }
        throw Error(response.error.message);
      })
      .catch((error) => {
        throw error;
      });
  }

  // prepareImage triggers (or re-triggers) a local build of a non-release image and
  // returns the current build state.
  public prepareImage(version: string, buildType: string): Promise<ImageStatusResponse> {
    const requestMetadata = {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        version,
        build_type: buildType,
      }),
    };

    return fetch(`${this.apiBaseUrl}images/prepare`, requestMetadata)
      .then((response) => response.json())
      .then((response) => {
        if (response.result) {
          return {
            state: response.result.state,
            detail: response.result.detail,
            logs: response.result.logs,
            error: response.result.error,
          };
        }
        throw Error(response.error.message);
      })
      .catch((error) => {
        throw error;
      });
  }

  public getImageStatus(version: string, buildType: string): Promise<ImageStatusResponse> {
    // The status response changes while an image is building. Use a unique URL as
    // well as the browser cache directive so intermediary caches cannot serve a
    // previous poll result.
    const params = new URLSearchParams({
      version,
      build_type: buildType,
      _cache_bust: Date.now().toString(),
    });

    return fetch(`${this.apiBaseUrl}images/status?${params.toString()}`, { cache: 'no-store' })
      .then((response) => response.json())
      .then((response) => {
        if (response.result) {
          return {
            state: response.result.state,
            detail: response.result.detail,
            logs: response.result.logs,
            error: response.result.error,
          };
        }
        throw Error(response.error.message);
      })
      .catch((error) => {
        throw error;
      });
  }

  public runQuery(
    query: string,
    version: string,
    buildType: string,
    format: string,
  ): Promise<RunQueryResponse> {
    const requestMetadata = {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        query,
        version,
        build_type: buildType,
        include_raw_output: true,
        settings: {
          clickhouse: {
            output_format: format,
          },
        },
      }),
    };

    return fetch(`${this.apiBaseUrl}runs`, requestMetadata)
      .then((response) => response.json())
      .then((response) => {
        if (response.result) {
          return {
            queryRunId: response.result.query_run_id,
            output: response.result.output,
            outputBytes: decodeOutputBytes(response.result.output, response.result.output_base64),
            outputTruncated: response.result.output_truncated === true,
            outputDropped: response.result.output_dropped === true,
            timeElapsed: response.result.time_elapsed,
          };
        }
        throw Error(response.error.message);
      })
      .catch((error) => {
        throw error;
      });
  }

  public getQueryRun(id: string): Promise<GetQueryRunResponse> {
    return fetch(`${this.apiBaseUrl}runs/${id}?include_raw_output=true`)
      .then((response) => response.json())
      .then((response) => {
        if (response.result) {
          return {
            version: response.result.version,
            buildType: response.result.build_type || RELEASE_BUILD_TYPE,
            input: response.result.input,
            output: response.result.output,
            outputBytes: decodeOutputBytes(response.result.output, response.result.output_base64),
            outputTruncated: response.result.output_truncated === true,
            outputDropped: response.result.output_dropped === true,
            queryRunId: response.result.query_run_id,
          };
        }
        throw Error(response.error.message);
      })
      .catch((error) => {
        throw error;
      });
  }
}
