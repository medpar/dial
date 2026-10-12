import Foundation

/// Watches a few files with kqueue (DispatchSource) and calls back, debounced,
/// when any changes. Files replaced by atomic rename are re-opened, so the
/// watch survives the CLIs' write-then-rename saves. Costs nothing while idle.
final class FileWatcher {
    private let paths: [String]
    private let debounce: TimeInterval
    private let handler: () -> Void
    private var sources: [String: DispatchSourceFileSystemObject] = [:]
    private var pending: DispatchWorkItem?
    private let queue = DispatchQueue(label: "dial.filewatcher")

    init(paths: [String], debounce: TimeInterval = 1.5, handler: @escaping () -> Void) {
        self.paths = paths
        self.debounce = debounce
        self.handler = handler
        queue.async { paths.forEach(self.arm) }
    }

    private func arm(_ path: String) {
        sources[path]?.cancel()
        sources[path] = nil
        let fd = open(path, O_EVTONLY)
        guard fd >= 0 else {
            // Not there yet (app never run, logged out): look again later.
            queue.asyncAfter(deadline: .now() + 30) { [weak self] in self?.arm(path) }
            return
        }
        let src = DispatchSource.makeFileSystemObjectSource(fileDescriptor: fd, eventMask: [.write, .rename, .delete, .extend], queue: queue)
        src.setEventHandler { [weak self, weak src] in
            guard let self, let src else { return }
            if !src.data.intersection([.rename, .delete]).isEmpty {
                // Atomic save replaced the file: watch the new one.
                self.queue.asyncAfter(deadline: .now() + 0.3) { self.arm(path) }
            }
            self.fire()
        }
        src.setCancelHandler { close(fd) }
        sources[path] = src
        src.resume()
    }

    private func fire() {
        pending?.cancel()
        let item = DispatchWorkItem { [handler] in DispatchQueue.main.async(execute: handler) }
        pending = item
        queue.asyncAfter(deadline: .now() + debounce, execute: item)
    }
}
