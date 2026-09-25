// stockx-ticker：macOS 系统级期货浮窗。
//
// 为什么需要它：网页里的浮窗只能活在浏览器页面里，切到别的应用就看不见了 —— 这是浏览器
// 沙箱的硬限制。这个工具用 AppKit 开一个**原生置顶窗口**（.floating + 所有 Space 可见），
// 所以切到任何应用、任何桌面都一直在。
//
// 数据来源就是本机已有的接口：GET {server}/api/futures/quotes?symbols=JM0,RB0
// 配置写在 ~/.stockx-ticker.json（首次运行自动生成带注释的默认配置）。
//
// 用法：
//   swiftc -O -o build/stockx-ticker main.swift   # 编译
//   ./build/stockx-ticker                          # 运行（菜单栏有图标，可退出）
//   ./build/stockx-ticker --print                   # 只抓一次打印到终端（无窗口，便于排查）

import AppKit
import Foundation

// MARK: - 配置

struct Config: Codable {
    var server: String = "http://127.0.0.1:8080"
    var symbols: [String] = []
    var count: Int = 5
    var opacity: Double = 0.94
    var intervalSec: Double = 3
    var showBidAsk: Bool = false
    var x: Double? = nil
    var y: Double? = nil
}

let configPath = ("~/.stockx-ticker.json" as NSString).expandingTildeInPath

func loadConfig() -> Config {
    let fm = FileManager.default
    if let data = fm.contents(atPath: configPath),
       let cfg = try? JSONDecoder().decode(Config.self, from: data) {
        return cfg
    }
    // 首次运行：写一份默认配置，用户照着改就行
    let cfg = Config()
    saveConfig(cfg)
    return cfg
}

func saveConfig(_ cfg: Config) {
    let enc = JSONEncoder()
    enc.outputFormatting = [.prettyPrinted, .sortedKeys]
    if let data = try? enc.encode(cfg) {
        try? data.write(to: URL(fileURLWithPath: configPath))
    }
}

// MARK: - 接口数据结构（对应 /api/futures/quotes）

struct Quote: Decodable {
    let symbol: String
    let name: String?
    let price: Double?
    let hold: Double?
    let volume: Double?
    let bid: Double?
    let ask: Double?
    let bid_vol: Double?
    let ask_vol: Double?
    let change_pct: Double?
    let prev_close: Double?
    let time: String?
    let source: String?
    let error: String?
}

struct Envelope: Decodable {
    let ok: Bool
    let data: [Quote]?
    let error: String?
}

// MARK: - 格式化

// 保留真实精度：1159.5 就是 1159.5（焦煤最小变动 0.5），3116 就显示 3116，
// 所以统一留 1 位小数再去掉没用的 ".0" —— 别按"大于 1000 就取整"把半价抹掉。
func fmtPrice(_ v: Double?) -> String {
    guard let v, v > 0 else { return "—" }
    var s = String(format: "%.1f", v)
    if s.hasSuffix(".0") { s.removeLast(2) }
    return s
}

func fmtHold(_ v: Double?) -> String {
    guard let v, v > 0 else { return "—" }
    return v >= 10000 ? String(format: "%.2f万", v / 10000) : String(format: "%.0f", v)
}

// 涨绿跌红（与页面里的浮窗保持一致）
func changeColor(_ pct: Double?) -> NSColor {
    guard let pct else { return .secondaryLabelColor }
    return pct >= 0 ? NSColor.systemGreen : NSColor.systemRed
}

// MARK: - 抓数据

func fetchQuotes(_ cfg: Config) async throws -> [Quote] {
    guard !cfg.symbols.isEmpty else { return [] }
    let syms = cfg.symbols.prefix(5).joined(separator: ",")
    guard var comp = URLComponents(string: cfg.server + "/api/futures/quotes") else {
        throw NSError(domain: "ticker", code: 1, userInfo: [NSLocalizedDescriptionKey: "server 地址不合法"])
    }
    comp.queryItems = [URLQueryItem(name: "symbols", value: syms)]
    guard let url = comp.url else {
        throw NSError(domain: "ticker", code: 1, userInfo: [NSLocalizedDescriptionKey: "URL 拼接失败"])
    }
    let (data, _) = try await URLSession.shared.data(from: url)
    let env = try JSONDecoder().decode(Envelope.self, from: data)
    if !env.ok {
        throw NSError(domain: "ticker", code: 2, userInfo: [NSLocalizedDescriptionKey: env.error ?? "接口返回失败"])
    }
    return env.data ?? []
}

// MARK: - --print 模式（无窗口，便于排查）

func printOnce(_ cfg: Config) async {
    if cfg.symbols.isEmpty {
        print("还没配品种：编辑 \(configPath) 里的 symbols（例如 [\"JM0\",\"RB0\"]）")
        return
    }
    do {
        let quotes = try await fetchQuotes(cfg)
        print("server=\(cfg.server) symbols=\(cfg.symbols.joined(separator: ","))")
        for q in quotes {
            let chg: String
            if let err = q.error, !err.isEmpty {
                _ = err
                chg = "—"
            } else {
                chg = q.change_pct.map { String(format: "%+.2f%%", $0) } ?? "—"
            }
            let book: String
            if cfg.showBidAsk, let bid = q.bid, bid > 0 {
                book = "  买一 \(fmtPrice(q.bid))×\(fmtHold(q.bid_vol))  卖一 \(fmtPrice(q.ask))×\(fmtHold(q.ask_vol))"
            } else {
                book = ""
            }
            let err = q.error.map { "  [\($0)]" } ?? ""
            print("\(q.symbol)  \(q.name ?? "")  价 \(fmtPrice(q.price))  涨跌 \(chg)  持仓 \(fmtHold(q.hold))  源 \(q.source ?? "—")\(book)\(err)")
        }
    } catch {
        print("取数失败：\(error.localizedDescription)")
    }
}

// MARK: - 窗口

final class RowView: NSView {
    let nameLabel = NSTextField(labelWithString: "")
    let priceLabel = NSTextField(labelWithString: "")
    let changeLabel = NSTextField(labelWithString: "")
    let holdLabel = NSTextField(labelWithString: "")
    let bookLabel = NSTextField(labelWithString: "")
    private let bookRow: NSStackView

    override init(frame: NSRect) {
        let line = NSStackView(views: [nameLabel, priceLabel, changeLabel, holdLabel])
        line.orientation = .horizontal
        line.spacing = 4
        line.distribution = .fill
        for l in [nameLabel, priceLabel, changeLabel, holdLabel] {
            l.font = .monospacedDigitSystemFont(ofSize: 12, weight: .regular)
            l.textColor = .labelColor
            l.lineBreakMode = .byTruncatingTail
        }
        bookLabel.font = .monospacedDigitSystemFont(ofSize: 10, weight: .regular)
        bookLabel.textColor = .secondaryLabelColor
        bookRow = NSStackView(views: [bookLabel])
        super.init(frame: frame)
        nameLabel.widthAnchor.constraint(equalToConstant: 62).isActive = true
        priceLabel.widthAnchor.constraint(equalToConstant: 52).isActive = true
        changeLabel.widthAnchor.constraint(equalToConstant: 54).isActive = true
        holdLabel.widthAnchor.constraint(equalToConstant: 54).isActive = true
        priceLabel.alignment = .right
        changeLabel.alignment = .right
        holdLabel.alignment = .right

        let col = NSStackView(views: [line, bookRow])
        col.orientation = .vertical
        col.alignment = .leading
        col.spacing = 1
        col.translatesAutoresizingMaskIntoConstraints = false
        addSubview(col)
        NSLayoutConstraint.activate([
            col.leadingAnchor.constraint(equalTo: leadingAnchor),
            col.trailingAnchor.constraint(equalTo: trailingAnchor),
            col.topAnchor.constraint(equalTo: topAnchor),
            col.bottomAnchor.constraint(equalTo: bottomAnchor),
        ])
        bookRow.isHidden = true
    }

    required init?(coder: NSCoder) { fatalError() }

    func apply(_ q: Quote, ref: Quote?, showBidAsk: Bool) {
        nameLabel.stringValue = q.name ?? q.symbol
        priceLabel.stringValue = fmtPrice(q.price)
        if let err = q.error, !err.isEmpty {
            priceLabel.textColor = .tertiaryLabelColor
            changeLabel.stringValue = "—"
            changeLabel.textColor = .tertiaryLabelColor
            holdLabel.stringValue = "—"
            toolTip = "\(q.symbol)：\(err)"
        } else {
            priceLabel.textColor = .labelColor
            changeLabel.stringValue = q.change_pct.map { String(format: "%+.2f%%", $0) } ?? "—"
            changeLabel.textColor = changeColor(q.change_pct)
            holdLabel.stringValue = fmtHold(q.hold)
            let src = q.source ?? "—"
            toolTip = "\(q.name ?? q.symbol)  最新 \(fmtPrice(q.price))  昨收 \(fmtPrice(q.prev_close))  持仓 \(fmtHold(q.hold))  成交量 \(fmtHold(q.volume))  更新 \(q.time ?? "—")  来源 \(src)"
        }
        holdLabel.textColor = .secondaryLabelColor
        nameLabel.textColor = .secondaryLabelColor

        let hasBook = showBidAsk && (q.bid ?? 0) > 0
        bookRow.isHidden = !showBidAsk
        if showBidAsk {
            bookLabel.stringValue = hasBook
                ? "买一 \(fmtPrice(q.bid))×\(fmtHold(q.bid_vol))　卖一 \(fmtPrice(q.ask))×\(fmtHold(q.ask_vol))"
                : "买一 —　卖一 —（暂无盘口）"
        }
    }
}

final class BackgroundView: NSView {
    override func draw(_ dirtyRect: NSRect) {
        NSColor.black.withAlphaComponent(0.82).setFill()
        let path = NSBezierPath(roundedRect: bounds, xRadius: 8, yRadius: 8)
        path.fill()
    }
}

@MainActor
final class TickerApp: NSObject, NSApplicationDelegate {
    private var cfg = loadConfig()
    private var window: NSWindow!
    private var statusItem: NSStatusItem!
    private var titleLabel: NSTextField!
    private var footerLabel: NSTextField!
    private var rows: [RowView] = []
    private var timer: Timer?
    private var lastQuotes: [String: Quote] = [:]
    private var lastError: String = ""

    func applicationDidFinishLaunching(_ notification: Notification) {
        buildMenu()
        buildWindow()
        buildStatusItem()
        startPolling()
    }

    func applicationWillTerminate(_ notification: Notification) {
        persistPosition()
    }

    // 菜单栏图标：置顶窗口没法放关闭按钮，退出/显隐都从这里走
    private func buildStatusItem() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        statusItem.button?.title = "期"
        let menu = NSMenu()
        menu.addItem(withTitle: "显示 / 隐藏浮窗", action: #selector(toggleWindow), keyEquivalent: "h").target = self
        menu.addItem(withTitle: "立即刷新", action: #selector(refreshNow), keyEquivalent: "r").target = self
        menu.addItem(withTitle: "编辑配置…", action: #selector(openConfig), keyEquivalent: ",").target = self
        menu.addItem(.separator())
        menu.addItem(withTitle: "退出", action: #selector(quit), keyEquivalent: "q").target = self
        statusItem.menu = menu
    }

    private func buildMenu() {
        let main = NSMenu()
        let appItem = NSMenuItem()
        main.addItem(appItem)
        let sub = NSMenu()
        sub.addItem(withTitle: "退出 stockx-ticker", action: #selector(quit), keyEquivalent: "q").target = self
        appItem.submenu = sub
        NSApp.mainMenu = main
    }

    private func buildWindow() {
        let width: CGFloat = cfg.showBidAsk ? 250 : 236
        let rowH: CGFloat = cfg.showBidAsk ? 32 : 18
        let height = 30 + rowH * CGFloat(max(1, min(5, cfg.count))) + 16
        let screen = NSScreen.main?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1440, height: 900)
        let x = cfg.x.map { CGFloat($0) } ?? (screen.maxX - width - 16)
        let y = cfg.y.map { CGFloat($0) } ?? (screen.maxY - height - 12)

        window = NSWindow(
            contentRect: NSRect(x: x, y: y, width: width, height: height),
            styleMask: [.borderless],
            backing: .buffered,
            defer: false
        )
        // 关键：置顶 + 所有 Space / 全屏应用都可见 → 切到别的应用也不消失
        window.level = .floating
        window.collectionBehavior = [.canJoinAllSpaces, .stationary, .fullScreenAuxiliary]
        window.isMovableByWindowBackground = true
        window.hasShadow = true
        window.alphaValue = cfg.opacity
        window.backgroundColor = .clear
        window.isOpaque = false

        let bg = BackgroundView(frame: NSRect(x: 0, y: 0, width: width, height: height))
        let stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 3
        stack.translatesAutoresizingMaskIntoConstraints = false

        titleLabel = NSTextField(labelWithString: "期货浮窗")
        titleLabel.font = .systemFont(ofSize: 11, weight: .semibold)
        titleLabel.textColor = .white
        stack.addArrangedSubview(titleLabel)

        for _ in 0..<max(1, min(5, cfg.count)) {
            let row = RowView(frame: .zero)
            rows.append(row)
            stack.addArrangedSubview(row)
        }

        footerLabel = NSTextField(labelWithString: "价 / 涨跌 / 持仓(万)")
        footerLabel.font = .systemFont(ofSize: 10)
        footerLabel.textColor = NSColor.white.withAlphaComponent(0.35)
        stack.addArrangedSubview(footerLabel)

        bg.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: bg.leadingAnchor, constant: 8),
            stack.trailingAnchor.constraint(lessThanOrEqualTo: bg.trailingAnchor, constant: -8),
            stack.topAnchor.constraint(equalTo: bg.topAnchor, constant: 7),
        ])
        window.contentView = bg
        // 拖动结束后记住位置
        NotificationCenter.default.addObserver(
            self, selector: #selector(persistPosition),
            name: NSWindow.didMoveNotification, object: window
        )
        window.orderFrontRegardless()
    }

    private func startPolling() {
        timer?.invalidate()
        refreshNow()
        timer = Timer.scheduledTimer(withTimeInterval: max(0.5, cfg.intervalSec), repeats: true) { [weak self] _ in
            Task { @MainActor in self?.refreshNow() }
        }
    }

    @objc private func refreshNow() {
        Task { @MainActor in
            do {
                let quotes = try await fetchQuotes(cfg)
                lastError = ""
                for q in quotes { lastQuotes[q.symbol] = q }
                render(quotes)
            } catch {
                lastError = error.localizedDescription
                render(cfg.symbols.compactMap { lastQuotes[$0] })   // 失败时保住上一次的价格
            }
        }
    }

    private func render(_ quotes: [Quote]) {
        if cfg.symbols.isEmpty {
            titleLabel.stringValue = "期货浮窗 · 未配置"
            rows.forEach { $0.isHidden = true }
            footerLabel.stringValue = "编辑 \(configPath) 填 symbols"
            return
        }
        rows.forEach { $0.isHidden = false }
        let bySymbol = Dictionary(uniqueKeysWithValues: quotes.map { ($0.symbol, $0) })
        for (i, row) in rows.enumerated() {
            let sym = cfg.symbols.count > i ? cfg.symbols[i] : ""
            if sym.isEmpty {
                row.isHidden = true
                row.apply(Quote(symbol: "", name: nil, price: nil, hold: nil, volume: nil, bid: nil, ask: nil, bid_vol: nil, ask_vol: nil, change_pct: nil, prev_close: nil, time: nil, source: nil, error: nil), ref: nil, showBidAsk: false)
                continue
            }
            let q = bySymbol[sym] ?? Quote(symbol: sym, name: sym, price: nil, hold: nil, volume: nil, bid: nil, ask: nil, bid_vol: nil, ask_vol: nil, change_pct: nil, prev_close: nil, time: nil, source: nil, error: lastError.isEmpty ? nil : lastError)
            row.apply(q, ref: lastQuotes[sym], showBidAsk: cfg.showBidAsk)
        }
        let head = cfg.symbols.first.flatMap { bySymbol[$0]?.price }.map { fmtPrice($0) } ?? ""
        titleLabel.stringValue = head.isEmpty ? "期货浮窗" : "期货浮窗 · \(head)"
        footerLabel.stringValue = lastError.isEmpty
            ? "价 / 涨跌 / 持仓(万)　\(Date().formatted(date: .omitted, time: .standard))"
            : "取数失败：\(lastError)"
    }

    @objc private func toggleWindow() {
        if window.isVisible {
            window.orderOut(nil)
        } else {
            window.orderFrontRegardless()
        }
    }

    @objc private func openConfig() {
        NSWorkspace.shared.open(URL(fileURLWithPath: configPath))
    }

    @objc private func persistPosition() {
        guard let window else { return }
        cfg.x = Double(window.frame.origin.x)
        cfg.y = Double(window.frame.origin.y)
        saveConfig(cfg)
    }

    @objc private func quit() {
        persistPosition()
        NSApp.terminate(nil)
    }
}

// MARK: - 入口

let args = CommandLine.arguments

// --symbols=JM0,RB0：把品种写进配置并落盘（省得手改 JSON），之后正常按配置跑
if let raw = args.first(where: { $0.hasPrefix("--symbols=") }) {
    var cfg = loadConfig()
    let list = raw.replacingOccurrences(of: "--symbols=", with: "")
        .split(separator: ",")
        .map { $0.trimmingCharacters(in: .whitespaces).uppercased() }
        .filter { !$0.isEmpty }
    cfg.symbols = Array(list.prefix(5))
    saveConfig(cfg)
    print("已保存品种：\(cfg.symbols.joined(separator: ","))（最多 5 个）")
}

if args.contains("--print") {
    let cfg = loadConfig()
    let sem = DispatchSemaphore(value: 0)
    Task {
        await printOnce(cfg)
        sem.signal()
    }
    sem.wait()
    exit(0)
}

// 顶层代码在 Swift 6 里不算 main actor 隔离，这里显式声明（AppKit 必须在主线程）
MainActor.assumeIsolated {
    let app = NSApplication.shared
    let delegate = TickerApp()
    app.delegate = delegate
    app.setActivationPolicy(.accessory) // 不占 Dock
    app.run()
}
