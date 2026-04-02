import SwiftUI
import UniformTypeIdentifiers
import UIKit

nonisolated struct LoadedSharedImage: Sendable {
    let data: Data
    let filename: String
    let mimeType: String
}

nonisolated enum ShareExtensionError: LocalizedError {
    case unsupportedContent
    case unreadableImage

    var errorDescription: String? {
        switch self {
        case .unsupportedContent:
            return "This share only supports images."
        case .unreadableImage:
            return "Unable to read the shared image."
        }
    }
}

@MainActor @Observable
final class ShareExtensionViewModel {
    var prompt = ""
    var availableVMs: [SharedVMSummary] = []
    var selectedVMName: String?
    var previewImage: UIImage?
    var error: String?
    var isRefreshingVMs = false
    var isLoadingImage = true
    var isSending = false
    var hasAuthToken = false

    private let extensionItems: [NSExtensionItem]
    private var loadedImage: LoadedSharedImage?
    private var didLoad = false

    init(extensionItems: [NSExtensionItem]) {
        self.extensionItems = extensionItems
    }

    var eligibleVMs: [SharedVMSummary] {
        availableVMs
            .filter(\.canReceiveSharedScreenshot)
            .sorted { lhs, rhs in
                lhs.vmName.localizedCaseInsensitiveCompare(rhs.vmName) == .orderedAscending
            }
    }

    var selectedVM: SharedVMSummary? {
        guard let selectedVMName else { return nil }
        return eligibleVMs.first(where: { $0.vmName == selectedVMName })
    }

    var canSend: Bool {
        hasAuthToken && loadedImage != nil && selectedVM != nil && !isSending && !isLoadingImage
    }

    func loadIfNeeded() async {
        guard !didLoad else { return }
        didLoad = true

        hasAuthToken = AuthTokenStore.loadToken() != nil
        applyVMs(SharedVMStore.loadVMs())

        do {
            let image = try await Self.loadSharedImage(from: extensionItems)
            loadedImage = image
            previewImage = UIImage(data: image.data)
        } catch {
            self.error = error.localizedDescription
        }
        isLoadingImage = false

        guard hasAuthToken else {
            if error == nil {
                error = ShareUploadError.missingAuth.localizedDescription
            }
            return
        }

        isRefreshingVMs = true
        defer { isRefreshingVMs = false }

        do {
            let refreshedVMs = try await ShareNetworkClient.listVMs(
                token: AuthTokenStore.loadToken() ?? ""
            )
            applyVMs(refreshedVMs)
        } catch {
            if eligibleVMs.isEmpty && self.error == nil {
                self.error = error.localizedDescription
            }
        }
    }

    func send() async throws {
        guard let image = loadedImage, let vm = selectedVM else { return }

        isSending = true
        defer { isSending = false }

        do {
            try await ShareUploadManager.shared.enqueueShare(
                vm: vm,
                prompt: prompt,
                imageData: image.data,
                filename: image.filename,
                mimeType: image.mimeType
            )
        } catch {
            self.error = error.localizedDescription
            throw error
        }
    }

    private func applyVMs(_ vms: [SharedVMSummary]) {
        availableVMs = vms

        let eligible = eligibleVMs
        guard !eligible.isEmpty else {
            selectedVMName = nil
            return
        }

        if let selectedVMName,
           eligible.contains(where: { $0.vmName == selectedVMName }) {
            return
        }

        if let lastShared = SharedVMStore.loadLastSharedVMName(),
           eligible.contains(where: { $0.vmName == lastShared }) {
            selectedVMName = lastShared
            return
        }

        selectedVMName = eligible.first?.vmName
    }

    private nonisolated static func loadSharedImage(from items: [NSExtensionItem]) async throws -> LoadedSharedImage {
        for item in items {
            guard let attachments = item.attachments else { continue }
            for provider in attachments {
                guard let typeIdentifier = imageTypeIdentifier(for: provider) else { continue }

                if let fileImage = try? await loadImageFromFile(
                    provider: provider,
                    typeIdentifier: typeIdentifier
                ) {
                    return fileImage
                }
                if let dataImage = try? await loadImageFromData(
                    provider: provider,
                    typeIdentifier: typeIdentifier
                ) {
                    return dataImage
                }
                if let objectImage = try? await loadImageFromObject(provider: provider) {
                    return objectImage
                }
            }
        }

        throw ShareExtensionError.unsupportedContent
    }

    private nonisolated static func imageTypeIdentifier(for provider: NSItemProvider) -> String? {
        if let identifier = provider.registeredTypeIdentifiers.first(where: {
            UTType($0)?.conforms(to: .image) == true
        }) {
            return identifier
        }
        if provider.hasItemConformingToTypeIdentifier(UTType.image.identifier) {
            return UTType.image.identifier
        }
        return nil
    }

    private nonisolated static func loadImageFromFile(
        provider: NSItemProvider,
        typeIdentifier: String
    ) async throws -> LoadedSharedImage? {
        let suggestedName = provider.suggestedName
        return try await withCheckedThrowingContinuation {
            (continuation: CheckedContinuation<LoadedSharedImage?, Error>) in
            provider.loadFileRepresentation(forTypeIdentifier: typeIdentifier) { url, error in
                if let error {
                    continuation.resume(throwing: error)
                    return
                }
                guard let url else {
                    continuation.resume(returning: nil)
                    return
                }
                do {
                    let data = try Data(contentsOf: url)
                    let filename = url.lastPathComponent.isEmpty ? (suggestedName ?? "screenshot.png") : url.lastPathComponent
                    let mimeType = inferredMIMEType(typeIdentifier: typeIdentifier, filename: filename)
                    continuation.resume(returning: LoadedSharedImage(
                        data: data,
                        filename: filename,
                        mimeType: mimeType
                    ))
                } catch {
                    continuation.resume(throwing: error)
                }
            }
        }
    }

    private nonisolated static func loadImageFromData(
        provider: NSItemProvider,
        typeIdentifier: String
    ) async throws -> LoadedSharedImage? {
        let suggestedName = provider.suggestedName
        return try await withCheckedThrowingContinuation {
            (continuation: CheckedContinuation<LoadedSharedImage?, Error>) in
            provider.loadDataRepresentation(forTypeIdentifier: typeIdentifier) { data, error in
                if let error {
                    continuation.resume(throwing: error)
                    return
                }
                guard let data else {
                    continuation.resume(returning: nil)
                    return
                }
                let filename = suggestedName ?? "screenshot.png"
                let mimeType = inferredMIMEType(typeIdentifier: typeIdentifier, filename: filename)
                continuation.resume(returning: LoadedSharedImage(
                    data: data,
                    filename: filename,
                    mimeType: mimeType
                ))
            }
        }
    }

    private nonisolated static func loadImageFromObject(provider: NSItemProvider) async throws -> LoadedSharedImage? {
        let suggestedName = provider.suggestedName
        return try await withCheckedThrowingContinuation {
            (continuation: CheckedContinuation<LoadedSharedImage?, Error>) in
            provider.loadObject(ofClass: UIImage.self) { object, error in
                if let error {
                    continuation.resume(throwing: error)
                    return
                }
                guard let image = object as? UIImage,
                      let data = image.pngData()
                else {
                    continuation.resume(returning: nil)
                    return
                }
                continuation.resume(returning: LoadedSharedImage(
                    data: data,
                    filename: (suggestedName ?? "screenshot") + ".png",
                    mimeType: "image/png"
                ))
            }
        }
    }

    private nonisolated static func inferredMIMEType(typeIdentifier: String, filename: String) -> String {
        if let mimeType = UTType(typeIdentifier)?.preferredMIMEType {
            return mimeType
        }
        if let filenameType = UTType(filenameExtension: URL(fileURLWithPath: filename).pathExtension),
           let mimeType = filenameType.preferredMIMEType {
            return mimeType
        }
        return "image/png"
    }
}

struct ShareExtensionView: View {
    let viewModel: ShareExtensionViewModel
    let onCancel: () -> Void
    let onComplete: () -> Void

    var body: some View {
        NavigationStack {
            GeometryReader { geometry in
                VStack(spacing: 12) {
                    previewCard(maxHeight: previewHeight(for: geometry.size))
                    vmCard
                    promptCard
                    if let error = viewModel.error {
                        errorCard(error)
                    }
                    Spacer(minLength: 0)
                }
                .padding(16)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            }
            .navigationTitle("Share to exe.dev")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel", action: onCancel)
                        .disabled(viewModel.isSending)
                }
                ToolbarItem(placement: .confirmationAction) {
                    if viewModel.isSending {
                        ProgressView()
                    } else {
                        Button("Send") {
                            Task {
                                do {
                                    try await viewModel.send()
                                    onComplete()
                                } catch {
                                    // viewModel already surfaces the error
                                }
                            }
                        }
                        .disabled(!viewModel.canSend)
                    }
                }
            }
            .interactiveDismissDisabled(viewModel.isSending)
        }
        .task {
            await viewModel.loadIfNeeded()
        }
    }

    private func previewHeight(for size: CGSize) -> CGFloat {
        min(max(size.height * 0.3, 100), 180)
    }

    private func cardBackground() -> some ShapeStyle {
        Color(uiColor: .secondarySystemBackground)
    }

    @ViewBuilder
    private func previewCard(maxHeight: CGFloat) -> some View {
        ZStack {
            RoundedRectangle(cornerRadius: 16)
                .fill(cardBackground())

            if let previewImage = viewModel.previewImage {
                Image(uiImage: previewImage)
                    .resizable()
                    .scaledToFit()
                    .frame(maxWidth: .infinity, maxHeight: maxHeight - 20)
                    .clipShape(RoundedRectangle(cornerRadius: 12))
                    .padding(10)
            } else if viewModel.isLoadingImage {
                ProgressView()
            } else {
                Label("Image unavailable", systemImage: "photo.badge.exclamationmark")
                    .foregroundStyle(.secondary)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: maxHeight)
    }

    @ViewBuilder
    private var vmCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("VM")
                .font(.caption)
                .foregroundStyle(.secondary)

            if viewModel.eligibleVMs.isEmpty {
                if viewModel.isRefreshingVMs {
                    HStack {
                        Spacer()
                        ProgressView()
                        Spacer()
                    }
                    .frame(minHeight: 28)
                } else {
                    Label(
                        "No running VMs with Shelley are available.",
                        systemImage: "server.rack"
                    )
                    .foregroundStyle(.secondary)
                }
            } else {
                Menu {
                    ForEach(viewModel.eligibleVMs) { vm in
                        Button(vm.vmName) {
                            viewModel.selectedVMName = vm.vmName
                        }
                    }
                } label: {
                    HStack(spacing: 8) {
                        Text(viewModel.selectedVM?.vmName ?? "Select a VM")
                            .foregroundStyle(.primary)
                            .lineLimit(1)
                        Spacer()
                        Image(systemName: "chevron.up.chevron.down")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(.secondary)
                    }
                    .padding(.vertical, 2)
                }
            }
        }
        .padding(12)
        .background(cardBackground(), in: RoundedRectangle(cornerRadius: 16))
    }

    private var promptCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Prompt")
                .font(.caption)
                .foregroundStyle(.secondary)

            TextField("Optional prompt", text: Binding(
                get: { viewModel.prompt },
                set: { viewModel.prompt = $0 }
            ), axis: .vertical)
                .lineLimit(2...4)
        }
        .padding(12)
        .background(cardBackground(), in: RoundedRectangle(cornerRadius: 16))
    }

    private func errorCard(_ error: String) -> some View {
        Label(error, systemImage: "exclamationmark.triangle.fill")
            .foregroundStyle(.red)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(12)
            .background(cardBackground(), in: RoundedRectangle(cornerRadius: 16))
    }
}
